"""The periodic Slack user sync records each user's team (req a8535451).

The scheduled SlackUserInfoWorkflow runs the backfill activity, which derives
its (user, team) pairs from a DISTINCT select over stored slackmessage rows and
then looks up a profile per pair, so every synced SlackUser row carries its
team id.

The hazard this pins down: the derivation is message-derived, so the synced
user set is a FUNCTION OF WHICH MESSAGES GOT STORED -- it is downstream of the
message channel gate. A user who only ever speaks in an agent channel is never
synced, so a future resolution that consults the synced user table (today's
does not -- app_mention resolves through the keycloak link row) would silently
narrow the principal set and hand the Slack user FCM's service account. These
tests drive the real derivation from real message rows, so severing it fails
here rather than passing against a stubbed message source.
"""

import asyncio
from unittest.mock import Mock

from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import events
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    get_user_teams_from_messages,
    upsert_message,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
    SlackMessageCreate,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.db.job_activity import (
    backfill_teams_from_messages_activity,
    upsert_slack_user_creates_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack import (
    activity as slack_activity,
)

TEAM = "T_TEAM"
POLL_USER = "U_POLL_USER"
AGENT_ONLY_USER = "U_AGENT_ONLY_USER"


def _event(channel, user, ts="1700000000.000100", client_msg_id="cmid-1"):
    return {
        "type": "message",
        "channel": channel,
        "user": user,
        "ts": ts,
        "text": "hello",
        "team": TEAM,
        "client_msg_id": f"{client_msg_id}-{ts}",
    }


def _fake_slack_client(users):
    """A Slack Web API stand-in that only answers profile lookups.

    Deliberately no users_list/users_info: the sync is not supposed to
    enumerate users through the API, and the tests assert that it doesn't.
    """

    def _spec(spec):
        if spec is None:
            spec = Mock()
            spec.status_code = 404
            return spec
        response = Mock()
        response.status_code = 200
        response.get.side_effect = lambda key, default=None: (
            {"display_name": spec, "real_name": spec} if key == "profile" else default
        )
        return response

    client = Mock()
    client.team_id = TEAM
    client.team_info.return_value = {"ok": True, "team": {"id": TEAM}}
    client.users_profile_get.side_effect = lambda user: _spec(users.get(user))
    return client


def _run_sync(users, monkeypatch):
    """Run the real sync: teams from messages, pairs from messages, profiles
    from the Slack API, then the real upsert."""
    client = _fake_slack_client(users)
    monkeypatch.setattr(slack_activity, "get_slack_web_client", lambda: client)
    asyncio.run(backfill_teams_from_messages_activity())
    creates = asyncio.run(slack_activity.backfill_slack_user_info_activity())
    asyncio.run(upsert_slack_user_creates_activity(creates))
    return client, creates


def _synced(session):
    return {u.slack_id: u for u in session.exec(select(SlackUser)).all()}


def test_synced_user_rows_carry_their_team(slack_db, poll_channel, monkeypatch):
    upsert_message(
        SlackMessageCreate.from_slack_message_json(
            _event(poll_channel.slack_id, POLL_USER)
        )
    )

    client, creates = _run_sync({POLL_USER: "poll user"}, monkeypatch)

    assert [(c.slack_id, c.slack_team_slack_id) for c in creates] == [
        (POLL_USER, TEAM)
    ]
    users = _synced(slack_db)
    assert set(users) == {POLL_USER}
    assert users[POLL_USER].slack_team_slack_id == TEAM
    # the team id is also resolved to the slackteam row, not just recorded as text
    assert users[POLL_USER].slack_team_id is not None


def test_sync_does_not_enumerate_users_through_the_slack_api(
    slack_db, poll_channel, monkeypatch
):
    upsert_message(
        SlackMessageCreate.from_slack_message_json(
            _event(poll_channel.slack_id, POLL_USER)
        )
    )

    client, _ = _run_sync({POLL_USER: "poll user"}, monkeypatch)

    assert client.users_list.call_count == 0
    assert client.users_info.call_count == 0
    # exactly one profile lookup, for the one pair the messages yielded
    assert [c.kwargs["user"] for c in client.users_profile_get.call_args_list] == [
        POLL_USER
    ]


def test_synced_user_set_is_a_function_of_the_stored_messages(
    slack_db, poll_channel, routed_agent_channel, monkeypatch
):
    """The message channel gate is upstream of the sync.

    One user speaks in the music-poll channel (stored) and one only in an
    agent channel (dropped by the gate). The sync's user set is exactly the
    first user, so it is the stored messages -- not the whole workspace --
    that decide who FCM knows about.
    """
    events.handle_message(_event(poll_channel.slack_id, POLL_USER), Mock())
    events.handle_message(
        _event(routed_agent_channel.slack_id, AGENT_ONLY_USER, ts="1700000000.000200"),
        Mock(),
    )

    # the gate is what separates the two
    assert [
        r.slack_user_slack_id for r in slack_db.exec(select(SlackMessage)).all()
    ] == [POLL_USER]
    assert get_user_teams_from_messages(TEAM) == {(POLL_USER, TEAM)}

    _, creates = _run_sync({POLL_USER: "poll user", AGENT_ONLY_USER: "agent user"}, monkeypatch)

    assert {(c.slack_id, c.slack_team_slack_id) for c in creates} == {
        (POLL_USER, TEAM)
    }
    assert set(_synced(slack_db)) == {POLL_USER}


def test_a_user_who_never_spoke_in_a_poll_channel_is_never_synced(
    slack_db, routed_agent_channel, monkeypatch
):
    """Same user, opposite side of the gate: a message that the handler
    dropped yields no user row at all, even though the Slack API would
    happily answer a profile lookup for them."""
    events.handle_message(
        _event(routed_agent_channel.slack_id, AGENT_ONLY_USER), Mock()
    )
    assert list(slack_db.exec(select(SlackMessage)).all()) == []

    _, creates = _run_sync({AGENT_ONLY_USER: "agent user"}, monkeypatch)

    assert creates == []
    assert _synced(slack_db) == {}
