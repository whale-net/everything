"""Block-until-linked gate for the whagent app_mention handler.

A Slack app_mention from a user with no stored Slack->Keycloak mapping must
be blocked: the user gets an ephemeral one-time link prompt and NO agent
session is created (no SlackThreadAgentWorkflow, no slackthreadsession row),
so a blocked mention never leaves an orphaned ACTIVE thread behind.

Importing the handler module runs its @app.event decorator, which would build
a real Bolt App (and make a network auth.test call). We pre-seed the app
module's singleton with a fake whose decorators are passthroughs, so the
handler stays a plain callable we invoke directly.
"""

import logging
from unittest.mock import Mock

import pytest

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod


class _FakeApp:
    """Stand-in for slack_bolt App: every decorator is a passthrough."""

    def __getattr__(self, _name):
        def _maybe(*a, **k):
            # direct decorator use: @app.middleware  -> return fn unchanged
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            # factory use: @app.event("x") / @app.action("x") -> passthrough deco
            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    whagent,
)

CHANNEL = "C123"
TEAM = "T123"
LINKED_USER = "U_LINKED"
UNLINKED_USER = "U_UNLINKED"
THREAD_TS = "1700000000.000100"
FAKE_TOKEN = "secrettoken-abc123"


def _event(user: str):
    return {
        "type": "app_mention",
        "channel": CHANNEL,
        "ts": THREAD_TS,
        "user": user,
        "team": TEAM,
        "text": "<@BOT> hello there",
    }


@pytest.fixture
def handler_env(monkeypatch):
    """Patch every dependency the app_mention handler touches.

    Returns a namespace of the mocks so each test can assert on them.
    """
    say = Mock(name="say")
    client = Mock(name="client")

    # channel + agent link
    slack_channel = Mock(id=1)
    monkeypatch.setattr(whagent, "get_slack_channel", Mock(return_value=slack_channel))
    monkeypatch.setattr(whagent, "get_agent_id_for_channel", Mock(return_value="agent-x"))

    # identity DAL (default: unlinked)
    get_identity = Mock(return_value=None)
    mint = Mock(return_value=FAKE_TOKEN)
    monkeypatch.setattr(whagent, "get_keycloak_identity", get_identity)
    monkeypatch.setattr(whagent, "mint_link_token", mint)

    # temporal side effects
    start_workflow = Mock()
    monkeypatch.setattr(whagent, "start_workflow", start_workflow)
    # No pre-existing thread session -> a linked mention would proceed.
    monkeypatch.setattr(whagent, "get_thread_session", Mock(return_value=None))

    # whagent client + temporal naming
    whagent_client = Mock(ui_public_url="https://ui.example")
    monkeypatch.setattr(whagent, "get_whagent_client", Mock(return_value=whagent_client))
    monkeypatch.setattr(whagent, "get_app_env", Mock(return_value="test"))
    monkeypatch.setattr(whagent, "get_temporal_queue_name", Mock(return_value="fcm-test-main"))

    # default a configured web public url; individual tests override/remove it
    monkeypatch.setenv("FCM_WEB_PUBLIC_URL", "https://web.example/")

    return {
        "say": say,
        "client": client,
        "get_identity": get_identity,
        "mint": mint,
        "start_workflow": start_workflow,
    }


def _fire(env, event, body=None):
    whagent.handle_whagent_app_mention(
        event,
        env["say"],
        client=env["client"],
        body=body if body is not None else {"team_id": TEAM},
    )


def test_unlinked_user_blocked_with_ephemeral_and_no_session(handler_env):
    """Unlinked mention: ephemeral link prompt, no workflow, no thread session."""
    _fire(handler_env, _event(UNLINKED_USER))

    # identity looked up for the mentioning (team, user); no mapping found
    handler_env["get_identity"].assert_called_once_with(TEAM, UNLINKED_USER)

    # a token was minted for exactly this (team, user)
    handler_env["mint"].assert_called_once_with(TEAM, UNLINKED_USER)

    # ephemeral posted once, scoped to the mentioning user, carrying the link
    handler_env["client"].chat_postEphemeral.assert_called_once()
    kwargs = handler_env["client"].chat_postEphemeral.call_args.kwargs
    assert kwargs["channel"] == CHANNEL
    assert kwargs["user"] == UNLINKED_USER, "ephemeral must be scoped to the mentioning user"
    assert f"https://web.example/link/{FAKE_TOKEN}" in kwargs["text"]
    # not a plain channel post
    handler_env["client"].chat_postMessage.assert_not_called()
    handler_env["say"].assert_not_called()

    # the security-critical bit: no workflow, hence no SlackThreadAgentWorkflow
    # and no slackthreadsession row (no orphaned ACTIVE thread)
    handler_env["start_workflow"].assert_not_called()
    # and the handler returned before even consulting the thread-session table
    whagent.get_thread_session.assert_not_called()


def test_unlinked_user_blocked_even_when_web_url_unset(monkeypatch, handler_env):
    """FCM_WEB_PUBLIC_URL unset: still blocked, never started."""
    monkeypatch.delenv("FCM_WEB_PUBLIC_URL", raising=False)

    _fire(handler_env, _event(UNLINKED_USER))

    # no link to mint, but the gate still holds
    handler_env["mint"].assert_not_called()
    handler_env["start_workflow"].assert_not_called()
    whagent.get_thread_session.assert_not_called()
    handler_env["client"].chat_postEphemeral.assert_not_called()


def test_unlinked_link_token_never_logged(monkeypatch, handler_env, caplog):
    """The one-time link token must never appear in logs."""
    with caplog.at_level(logging.DEBUG):
        _fire(handler_env, _event(UNLINKED_USER))

    assert handler_env["mint"].called
    assert FAKE_TOKEN not in caplog.text, "link token must never be logged"


def test_linked_user_starts_workflow_as_before(handler_env):
    """A user with a stored mapping proceeds to the normal workflow start."""
    handler_env["get_identity"].return_value = Mock(keycloak_sub="kc-1")

    _fire(handler_env, _event(LINKED_USER))

    handler_env["get_identity"].assert_called_once_with(TEAM, LINKED_USER)
    handler_env["mint"].assert_not_called()
    handler_env["client"].chat_postEphemeral.assert_not_called()
    handler_env["say"].assert_not_called()
    handler_env["start_workflow"].assert_called_once()


def test_no_agent_link_replies_without_identity_lookup(handler_env):
    """No agent configured for the channel: plain reply, no identity work."""
    whagent.get_agent_id_for_channel.return_value = None

    _fire(handler_env, _event(UNLINKED_USER))

    handler_env["say"].assert_called_once()
    assert (
        handler_env["say"].call_args.kwargs["text"]
        == "No whagent-net agent is configured for this channel."
    )
    # must not touch the identity DAL at all
    handler_env["get_identity"].assert_not_called()
    handler_env["mint"].assert_not_called()
    handler_env["start_workflow"].assert_not_called()
    handler_env["client"].chat_postEphemeral.assert_not_called()


def test_token_bound_to_mentioning_identity(handler_env):
    """The minted token is bound to the mentioning (team_id, user_id)."""
    # distinct team/user to prove the handler passes the event's identity,
    # not some ambient/default value
    other = "T-OTHER"
    event = _event(UNLINKED_USER)
    event["team"] = other

    _fire(handler_env, event)

    handler_env["mint"].assert_called_once_with(other, UNLINKED_USER)
