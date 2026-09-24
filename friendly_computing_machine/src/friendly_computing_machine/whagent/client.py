"""gRPC client for whagent-net's SessionService, with service-account auth.

whagent_net/README.md "Client credentials (service accounts)": a
client_credentials caller drives SessionService exactly as a human
operator does -- StartSession/SendTurn/GetSession/ReadTranscript all take
the same `Authorization: Bearer <token>` a human's token would, minted
here via a client_credentials grant against Keycloak instead of a browser
sign-in.

`on_behalf_of` is left unset on every StartSessionRequest: per the
proto's own comment and the README's service-account section, a service
account acting on its own credential acts for itself, and unset means
"acting subject == on-behalf-of subject" (the M1 default `api` already
applies) -- there is no separate identity to set it to.

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
        self, agent_id: str, first_turn: Optional[str] = None
    ) -> session_pb2.Session:
        """Start a session, optionally sending its first turn.

        Mirrors whagent_net/mcp's start_session tool: StartSessionRequest
        carries no first-turn field of its own, so a non-empty first_turn
        is a second SendTurn call after StartSession succeeds.
        """
        resp = self._call(
            self._stub.StartSession,
            self._pb2.StartSessionRequest(agent_id=agent_id),
        )
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
    ) -> tuple[Optional[str], int]:
        """Return the most recent assistant_message event's text from
        from_seq on (if any), plus the position to resume reading from.

        Payload is per-type JSON (session.proto's TranscriptEvent doc
        comment); an unparseable or unexpected payload is skipped rather
        than raised, so one bad event doesn't take down the whole
        relay -- see this method's caller (the whagent temporal activity)
        for how that's surfaced to the user instead. Callers must pass the
        from_seq of the turn they're rendering, not always 0 -- a turn
        that ends without committing its own assistant_message (the
        mid-tool-loop cap trip) must not be rendered using an earlier
        turn's stale reply.
        """
        events, next_from_seq = self.read_transcript(session_id, from_seq=from_seq)
        text: Optional[str] = None
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
                text = content
        return text, next_from_seq


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
