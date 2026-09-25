"""Red/green coverage for fcm asserting a linked Slack user's Keycloak
identity on whagent-net StartSession (FR7/FR8, US2/US6).

Three guarantees, exercised against fakes rather than a live Temporal
worker or gRPC channel (this repo has no WorkflowEnvironment harness --
see test_whagent_workflow.py's note):

1. The client puts the linked (iss, sub) on StartSessionRequest.on_behalf_of
   with kind=human, and leaves the field unset for an unlinked start.
2. Every follow-up SendTurn stays under fcm's own service-account bearer,
   never a per-user credential (FR8).
3. The app_mention handler looks up the stored mapping and populates the
   workflow params only for a linked user.

Plus the two failure/replay edges: an old workflow payload lacking the new
fields still deserializes, and a PERMISSION_DENIED start surfaces as a
user-visible in-thread reply plus an ERROR log rather than a silent hang.
"""

import asyncio
import logging
from types import SimpleNamespace

import grpc
import pytest
from whagent_net.protos import session_pb2

from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent import (
    activity as activity_mod,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent import (
    workflow as workflow_mod,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.activity import (
    StartWhagentSessionParams,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.whagent.workflow import (
    SlackThreadAgentWorkflow,
    SlackThreadAgentWorkflowParams,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    WhagentClient,
)

# A linked user's (iss, sub) as stored by the identity DAL.
LINKED_ISS = "https://keycloak.example/realms/fcm"
LINKED_SUB = "keycloak-sub-abc123"


# ----------------------------------------------------------------------
# client: start_session populates / omits on_behalf_of (FR7)
# ----------------------------------------------------------------------


def _client_with_capturing_stub() -> tuple[WhagentClient, dict]:
    """A WhagentClient whose StartSession/SendTurn record their requests.

    WhagentClient.__init__ only builds a lazy insecure channel, and
    _get_token is replaced with a constant so no Keycloak round-trip is
    needed -- the captured `metadata` is still the real bearer the client
    would attach, which is what FR8 is asserted against below.
    """
    client = WhagentClient(
        api_url="localhost:0",
        keycloak_token_url="http://example.invalid/token",
        client_id="fcm-service-client",
        client_secret="secret",
    )
    client._get_token = lambda force_refresh=False: "fcm-service-bearer"
    captured: dict = {}

    class FakeStub:
        def StartSession(self, request, metadata=None):
            captured["start_request"] = request
            captured["start_metadata"] = metadata
            session = session_pb2.Session(session_id="sess-1", state=1)
            return session_pb2.StartSessionResponse(session=session)

        def SendTurn(self, request, metadata=None):
            captured["turn_request"] = request
            captured["turn_metadata"] = metadata
            session = session_pb2.Session(session_id="sess-1", state=2)
            return session_pb2.SendTurnResponse(session=session)

    client._stub = FakeStub()
    return client, captured


def test_start_session_asserts_linked_identity_with_human_kind():
    client, captured = _client_with_capturing_stub()

    client.start_session("agent-1", on_behalf_of=(LINKED_ISS, LINKED_SUB))

    subject = captured["start_request"].on_behalf_of
    assert subject.iss == LINKED_ISS
    assert subject.sub == LINKED_SUB
    # The whole point of delegation: whagent-net records a *human* subject,
    # not a service account.
    assert subject.kind == session_pb2.SUBJECT_KIND_HUMAN


def test_start_session_leaves_on_behalf_of_unset_when_not_given():
    client, captured = _client_with_capturing_stub()

    client.start_session("agent-1")

    # Unset (not merely empty) so whagent-net treats the session as
    # self-acting rather than as a delegation with a blank subject.
    assert not captured["start_request"].HasField("on_behalf_of")


def test_start_session_first_turn_does_not_reintroduce_per_user_identity():
    # A first_turn is a second SendTurn -- it must still ride the service
    # bearer, not the delegated subject.
    client, captured = _client_with_capturing_stub()

    client.start_session("agent-1", first_turn="hello", on_behalf_of=(LINKED_ISS, LINKED_SUB))

    assert captured["start_request"].on_behalf_of.sub == LINKED_SUB
    assert captured["turn_metadata"] == (("authorization", "Bearer fcm-service-bearer"),)


# ----------------------------------------------------------------------
# client: send_turn never uses a per-user credential (FR8)
# ----------------------------------------------------------------------


def test_send_turn_always_uses_service_account_bearer_not_per_user_identity():
    client, captured = _client_with_capturing_stub()

    # Start a delegated session, then continue it -- the follow-up turn must
    # still be authorized by fcm's own service credential.
    client.start_session("agent-1", on_behalf_of=(LINKED_ISS, LINKED_SUB))
    client.send_turn("sess-1", "another turn")

    # Same service-account bearer as StartSession -- no second identity crept in.
    assert captured["turn_metadata"] == (("authorization", "Bearer fcm-service-bearer"),)
    assert captured["turn_metadata"] == captured["start_metadata"]
    # SendTurnRequest carries no subject/identity field at all: the only
    # fields are session_id and input. This is the structural guarantee
    # behind FR8 -- there is nowhere to put a per-user credential.
    turn = captured["turn_request"]
    assert turn.session_id == "sess-1"
    assert turn.input == "another turn"
    assert not hasattr(turn, "on_behalf_of")
    assert not hasattr(turn, "subject")


# ----------------------------------------------------------------------
# handler: app_mention populates params for a linked user only
# ----------------------------------------------------------------------


@pytest.fixture
def whagent_handler(monkeypatch):
    """Import the app_mention handler with a stubbed Slack app.

    The handlers package registers each listener with @app.event at import
    time; the app proxy would otherwise demand a live Slack web client.
    A dummy app whose every attribute is a pass-through decorator lets the
    module import cleanly so handle_whagent_app_mention can be called
    directly.
    """
    from friendly_computing_machine.src.friendly_computing_machine.bot import app as bot_app

    class DummyApp:
        def __getattr__(self, _name):
            def factory(*_args, **_kwargs):
                def decorator(fn):
                    return fn

                return decorator

            return factory

    monkeypatch.setattr(bot_app, "get_slack_app", lambda: DummyApp())
    import importlib

    return importlib.import_module(
        "friendly_computing_machine.src.friendly_computing_machine.bot.handlers.whagent"
    )


def _mention_event(user: str = "U_LINKED") -> dict:
    return {
        "channel": "C123",
        "ts": "1712345678.000100",
        "team_id": "T_TEAM",
        "user": user,
        "text": "<@U_BOT> what is the status?",
    }


def _run_mention(handler, monkeypatch, identity) -> SlackThreadAgentWorkflowParams:
    """Drive handle_whagent_app_mention and capture the params it would start.

    `identity` is what get_keycloak_identity returns: a mapping object with
    keycloak_iss/keycloak_sub, or None for an unlinked user.
    """
    monkeypatch.setattr(
        handler, "get_slack_channel", lambda slack_channel_slack_id: SimpleNamespace(id=42)
    )
    monkeypatch.setattr(handler, "get_agent_id_for_channel", lambda channel_db_id: "agent-1")
    # No active thread session -> the handler proceeds to start a new workflow.
    monkeypatch.setattr(handler, "get_thread_session", lambda channel_db_id, thread_ts: None)
    monkeypatch.setattr(
        handler, "get_keycloak_identity", lambda team_id, slack_user_id: identity
    )
    monkeypatch.setattr(
        handler, "get_whagent_client", lambda: SimpleNamespace(ui_public_url="https://ui")
    )
    monkeypatch.setattr(handler, "get_app_env", lambda: "dev")
    monkeypatch.setattr(handler, "get_temporal_queue_name", lambda name: "queue")

    captured = {}

    def fake_start_workflow(fn, params, **kwargs):
        captured["params"] = params

    monkeypatch.setattr(handler, "start_workflow", fake_start_workflow)

    said: list = []
    handler.handle_whagent_app_mention(_mention_event(), lambda **kw: said.append(kw))

    assert captured, "handler did not attempt to start a workflow"
    return captured["params"]


def test_app_mention_passes_linked_identity_into_workflow_params(whagent_handler, monkeypatch):
    identity = SimpleNamespace(keycloak_iss=LINKED_ISS, keycloak_sub=LINKED_SUB)

    params = _run_mention(whagent_handler, monkeypatch, identity)

    assert params.on_behalf_of_iss == LINKED_ISS
    assert params.on_behalf_of_sub == LINKED_SUB
    assert params.first_message == "what is the status?"


def test_app_mention_leaves_on_behalf_of_unset_for_unlinked_user(whagent_handler, monkeypatch):
    params = _run_mention(whagent_handler, monkeypatch, None)

    assert params.on_behalf_of_iss is None
    assert params.on_behalf_of_sub is None


# ----------------------------------------------------------------------
# replay safety: an old payload lacking the new fields still deserializes
# ----------------------------------------------------------------------


def test_workflow_params_default_missing_on_behalf_of_fields_for_replay():
    # Simulates an in-flight history recorded before identity linking: the
    # payload has the original fields only. The None defaults are what keep
    # it deserializable instead of raising TypeError on a missing arg.
    params = SlackThreadAgentWorkflowParams(
        channel_slack_id="C123",
        channel_db_id=42,
        thread_ts="1712345678.000100",
        agent_id="agent-1",
        first_message="hi",
        slack_user_id="U_OLD",
        whagent_ui_public_url="https://ui",
    )

    assert params.on_behalf_of_iss is None
    assert params.on_behalf_of_sub is None


def test_old_params_run_start_non_delegated(monkeypatch):
    # The deserialized old params must drive a *non-delegated* start: the
    # workflow's iss/sub -> tuple projection yields None.
    starts: list[StartWhagentSessionParams] = []

    async def fake_execute_activity(fn, arg, **kwargs):
        if fn is workflow_mod.start_whagent_session_activity:
            starts.append(arg)
            return SimpleNamespace(session_id="sess-1")
        if fn is workflow_mod.insert_thread_session_activity:
            return 1
        if fn is workflow_mod.post_slack_thread_message_activity:
            return "1.0"
        if fn is workflow_mod.update_slack_message_activity:
            return "1.0"
        return None

    monkeypatch.setattr(workflow_mod.workflow, "execute_activity", fake_execute_activity)

    params = SlackThreadAgentWorkflowParams(
        channel_slack_id="C123",
        channel_db_id=42,
        thread_ts="1712345678.000100",
        agent_id="agent-1",
        first_message="hi",
        slack_user_id="U_OLD",
        whagent_ui_public_url="https://ui",
    )
    # Run exactly one turn and return (skipping the idle wait / turn queue),
    # so the assertion is purely about the start activity's on_behalf_of.
    wf = SlackThreadAgentWorkflow()
    wf._resolve_turn = _immediate_failed_turn
    wf._stop_requested = True
    asyncio.run(wf.run(params))

    assert starts
    assert starts[0].on_behalf_of is None


async def _immediate_failed_turn(session_id, since_seq):
    return ("turn failed", since_seq)


# ----------------------------------------------------------------------
# failure path: PERMISSION_DENIED start -> in-thread reply + ERROR log
# ----------------------------------------------------------------------


def test_permission_denied_start_surfaces_in_thread_reply_and_logs(monkeypatch, caplog):
    def fake_execute_activity(fn, arg, **kwargs):
        async def _run():
            if fn is workflow_mod.start_whagent_session_activity:
                raise _permission_denied_error()
            if fn is workflow_mod.post_slack_thread_message_activity:
                posted.append(arg)
                return "1.0"
            return None

        return _run()

    posted: list = []

    async def async_fake(fn, arg, **kwargs):
        return await fake_execute_activity(fn, arg, **kwargs)

    monkeypatch.setattr(workflow_mod.workflow, "execute_activity", async_fake)

    params = SlackThreadAgentWorkflowParams(
        channel_slack_id="C123",
        channel_db_id=42,
        thread_ts="1712345678.000100",
        agent_id="agent-1",
        first_message="hi",
        slack_user_id="U_LINKED",
        whagent_ui_public_url="https://ui",
        on_behalf_of_iss=LINKED_ISS,
        on_behalf_of_sub=LINKED_SUB,
    )
    wf = SlackThreadAgentWorkflow()

    with caplog.at_level(logging.ERROR, logger=workflow_mod.__name__):
        with pytest.raises(grpc.RpcError):
            asyncio.run(wf.run(params))

    # A clear, user-visible in-thread reply -- not silence.
    assert posted, "no in-thread reply posted on a denied start"
    reply = posted[0]
    assert reply.channel_id == "C123"
    assert reply.thread_ts == "1712345678.000100"
    assert "Couldn't start" in reply.text
    assert "permissions" in reply.text
    # And the reason is logged at ERROR for the operator.
    assert any(r.levelno == logging.ERROR for r in caplog.records)


def test_start_whagent_session_activity_logs_permission_denied(monkeypatch, caplog):
    # The activity is the layer that sees the raw PERMISSION_DENIED; it must
    # log a specific ERROR (not just re-raise) so the allowlist misconfig is
    # diagnosable from logs.

    def raise_permission_denied(*_args, **_kwargs):
        raise _permission_denied_error()

    fake_client = SimpleNamespace(start_session=raise_permission_denied)
    monkeypatch.setattr(activity_mod, "get_whagent_client", lambda: fake_client)

    params = StartWhagentSessionParams(
        agent_id="agent-1",
        first_turn="hi",
        on_behalf_of=(LINKED_ISS, LINKED_SUB),
    )

    with caplog.at_level(logging.ERROR, logger=activity_mod.__name__):
        with pytest.raises(grpc.RpcError):
            asyncio.run(activity_mod.start_whagent_session_activity(params))

    errors = [r for r in caplog.records if r.levelno == logging.ERROR]
    assert errors
    assert "allowlist" in errors[0].getMessage()


def _permission_denied_error() -> grpc.RpcError:
    class _Denied(grpc.RpcError):
        def code(self):
            return grpc.StatusCode.PERMISSION_DENIED

    return _Denied()
