"""gRPC client for whagent-net's SessionService, with service-account auth.

whagent_net/README.md "Client credentials (service accounts)": a
client_credentials caller drives SessionService exactly as a human
operator does -- StartSession/SendTurn/GetSession/ReadTranscript all take
the same `Authorization: Bearer <token>` a human's token would, minted
here via a client_credentials grant against Keycloak instead of a browser
sign-in.

`on_behalf_of` is unset by default (a service account acting on its own
credential acts for itself), but a caller whose Slack user has a linked
Keycloak identity may assert that identity on StartSession so whagent-net
records the session as running on behalf of that user. Only whagent-net's
allowlisted client_id may assert a non-empty `on_behalf_of`; every other
call -- including every follow-up SendTurn -- stays under this client's own
service-account credential. The Slack user never authenticates here: the
asserted subject is just data on the request, not a credential.

Uses a plain insecure gRPC channel. `api` sits behind ingress-terminated
TLS the same way every other in-repo caller of a sibling service reaches
it over a plain http:// URL (see manman/api.py's ManManExperienceAPI);
there's no evidence in whagent_net's docs that a caller needs client-side
TLS to reach `api` directly, so this matches that existing pattern rather
than adding a new one.
"""

from __future__ import annotations

import json
import logging
import time
from typing import Optional

import grpc
import httpx

from whagent_net.protos import session_pb2, session_pb2_grpc

logger = logging.getLogger(__name__)

# Refresh the cached token this many seconds before its reported expiry,
# to absorb clock skew and in-flight-call latency.
_TOKEN_EXPIRY_MARGIN_SECONDS = 30.0

# transcript_event.type for a model response (whagent_net/events/events.go's
# EventTypeAssistantMessage); payload shape is worker/context.go's
# transcriptMessagePayload -- {"role": "assistant", "content": "..."}.
_ASSISTANT_MESSAGE_EVENT_TYPE = "assistant_message"


class WhagentClient:
    """Thin wrapper around whagent-net's SessionService gRPC stub.

    Not thread-safe beyond what grpc.Channel/the token cache tolerate --
    matches this codebase's other lazily-initialized singleton clients
    (e.g. bot/app.py's get_slack_web_client()).
    """

    def __init__(
        self,
        api_url: str,
        keycloak_token_url: str,
        client_id: str,
        client_secret: str,
        ui_public_url: str = "",
    ):
        self.ui_public_url = ui_public_url

        self._token_url = keycloak_token_url
        self._client_id = client_id
        self._client_secret = client_secret

        self._pb2 = session_pb2

        self._channel = grpc.insecure_channel(api_url)
        self._stub = session_pb2_grpc.SessionServiceStub(self._channel)

        self._token: Optional[str] = None
        self._token_expiry: float = 0.0

    # ------------------------------------------------------------------
    # auth
    # ------------------------------------------------------------------

    def _fetch_token(self) -> str:
        response = httpx.post(
            self._token_url,
            data={
                "grant_type": "client_credentials",
                "client_id": self._client_id,
                "client_secret": self._client_secret,
            },
            timeout=10.0,
        )
        response.raise_for_status()
        body = response.json()
        expires_in = float(body.get("expires_in", 60))
        self._token_expiry = time.time() + expires_in
        logger.info("fetched whagent-net service-account token, expires_in=%s", expires_in)
        return body["access_token"]

    def _get_token(self, force_refresh: bool = False) -> str:
        if (
            force_refresh
            or self._token is None
            or time.time() >= self._token_expiry - _TOKEN_EXPIRY_MARGIN_SECONDS
        ):
            self._token = self._fetch_token()
        return self._token

    def _metadata(self, force_refresh: bool = False):
        return (("authorization", f"Bearer {self._get_token(force_refresh)}"),)

    def _call(self, method, request):
        try:
            return method(request, metadata=self._metadata())
        except grpc.RpcError as e:
            if e.code() != grpc.StatusCode.UNAUTHENTICATED:
                raise
            logger.warning(
                "whagent-net call unauthenticated, refetching token and retrying once"
            )
            return method(request, metadata=self._metadata(force_refresh=True))

    # ------------------------------------------------------------------
    # SessionService
    # ------------------------------------------------------------------

    def start_session(
        self,
        agent_id: str,
        first_turn: Optional[str] = None,
        on_behalf_of: Optional[tuple[str, str]] = None,
    ) -> session_pb2.Session:
        """Start a session, optionally sending its first turn.

        Mirrors whagent_net/mcp's start_session tool: StartSessionRequest
        carries no first-turn field of its own, so a non-empty first_turn
        is a second SendTurn call after StartSession succeeds.

        on_behalf_of is the linked user's (iss, sub) Keycloak identity, or
        None to leave the field unset (a non-delegated, self-acting start).
        The assertion is authorized by this client's own allowlisted
        client_id, not by any credential the Slack user holds.
        """
        request = self._pb2.StartSessionRequest(agent_id=agent_id)
        if on_behalf_of is not None:
            iss, sub = on_behalf_of
            request.on_behalf_of.CopyFrom(
                self._pb2.Subject(
                    iss=iss,
                    sub=sub,
                    kind=self._pb2.SUBJECT_KIND_HUMAN,
                )
            )
        resp = self._call(self._stub.StartSession, request)
        session = resp.session
        if first_turn:
            turn_resp = self._call(
                self._stub.SendTurn,
                self._pb2.SendTurnRequest(session_id=session.session_id, input=first_turn),
            )
            session = turn_resp.session
        return session

    def send_turn(self, session_id: str, input: str) -> session_pb2.Session:
        resp = self._call(
            self._stub.SendTurn,
            self._pb2.SendTurnRequest(session_id=session_id, input=input),
        )
        return resp.session

    def get_session(self, session_id: str) -> session_pb2.Session:
        resp = self._call(
            self._stub.GetSession,
            self._pb2.GetSessionRequest(session_id=session_id),
        )
        return resp.session

    def read_transcript(
        self, session_id: str, from_seq: int = 0
    ) -> tuple[list[session_pb2.TranscriptEvent], int]:
        resp = self._call(
            self._stub.ReadTranscript,
            self._pb2.ReadTranscriptRequest(session_id=session_id, from_seq=from_seq),
        )
        return list(resp.events), resp.next_from_seq

    def latest_assistant_message(
        self, session_id: str, from_seq: int = 0
    ) -> Optional[tuple[str, int]]:
        """Return the most recent assistant_message event's (text, seq) at or after from_seq.

        Callers use from_seq as a watermark -- events already consumed for
        a prior turn are excluded, so a hit here is proof a *new* reply has
        actually landed rather than a stale re-read of the previous turn's
        answer (see workflow.py's _resolve_turn for why that distinction
        matters).

        Payload is per-type JSON (session.proto's TranscriptEvent doc
        comment); an unparseable or unexpected payload is skipped rather
        than raised, so one bad event doesn't take down the whole
        relay -- see this method's caller (the whagent temporal activity)
        for how that's surfaced to the user instead.
        """
        events, _ = self.read_transcript(session_id, from_seq=from_seq)
        result: Optional[tuple[str, int]] = None
        for event in events:
            if event.type != _ASSISTANT_MESSAGE_EVENT_TYPE:
                continue
            try:
                payload = json.loads(event.payload)
            except (ValueError, TypeError):
                logger.warning(
                    "unparseable assistant_message payload, session=%s seq=%s",
                    session_id,
                    event.seq,
                )
                continue
            content = payload.get("content") if isinstance(payload, dict) else None
            if content:
                result = (content, event.seq)
        return result


# ----------------------------------------------------------------------
# module-level lazy singleton, matching bot/app.py's get_slack_web_client()
# ----------------------------------------------------------------------

_client: Optional[WhagentClient] = None


def init_whagent_client(
    api_url: str,
    keycloak_token_url: str,
    client_id: str,
    client_secret: str,
    ui_public_url: str = "",
) -> None:
    global _client
    if _client is not None:
        raise RuntimeError("double whagent client init")
    _client = WhagentClient(
        api_url=api_url,
        keycloak_token_url=keycloak_token_url,
        client_id=client_id,
        client_secret=client_secret,
        ui_public_url=ui_public_url,
    )


def get_whagent_client() -> WhagentClient:
    if _client is None:
        raise RuntimeError("whagent client not initialized")
    return _client
