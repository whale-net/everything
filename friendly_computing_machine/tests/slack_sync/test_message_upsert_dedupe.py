"""Redelivered message events do not duplicate rows (req dea2e4ba #2).

Slack retries deliveries, so the same event reaches the handler more than
once. The store is an upsert keyed on the client message id when the event
carries one, and otherwise on team + channel + timestamp.
"""

from unittest.mock import Mock

from sqlmodel import select

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import events
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    upsert_message,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
    SlackMessageCreate,
)

TEAM = "T_TEAM"
USER = "U_SOMEONE"
TS = "1700000000.000100"


def _event(channel, ts=TS, client_msg_id="cmid-1", text="hello"):
    event = {
        "type": "message",
        "channel": channel,
        "user": USER,
        "ts": ts,
        "text": text,
        "team": TEAM,
    }
    if client_msg_id is not None:
        event["client_msg_id"] = client_msg_id
    return event


def _stored(session):
    return list(session.exec(select(SlackMessage)).all())


def test_redelivered_event_with_client_msg_id_does_not_duplicate(
    slack_db, poll_channel
):
    event = _event(poll_channel.slack_id)

    events.handle_message(event, Mock())
    events.handle_message(event, Mock())
    events.handle_message(event, Mock())

    assert len(_stored(slack_db)) == 1


def test_redelivered_event_without_client_msg_id_does_not_duplicate(
    slack_db, poll_channel
):
    """Falls back to the team + channel + timestamp key."""
    event = _event(poll_channel.slack_id, client_msg_id=None)

    events.handle_message(event, Mock())
    events.handle_message(event, Mock())

    rows = _stored(slack_db)
    assert len(rows) == 1
    assert rows[0].slack_id is None


def test_same_client_msg_id_is_one_row_across_channels(slack_db, poll_channel):
    """The client msg id key wins over the channel, so a redelivery that
    arrives with a different channel or ts still folds onto the stored row."""
    upsert_message(
        SlackMessageCreate.from_slack_message_json(_event(poll_channel.slack_id))
    )
    upsert_message(
        SlackMessageCreate.from_slack_message_json(
            _event("C_SOMEWHERE_ELSE", ts="1700000000.000999")
        )
    )

    assert len(_stored(slack_db)) == 1


def test_distinct_messages_both_land(slack_db, poll_channel):
    """The dedupe key is not over-broad: two different messages are two rows."""
    events.handle_message(_event(poll_channel.slack_id, ts=TS), Mock())
    events.handle_message(
        _event(poll_channel.slack_id, ts="1700000000.000200", client_msg_id="cmid-2"),
        Mock(),
    )

    assert len(_stored(slack_db)) == 2


def test_upsert_of_redelivery_updates_the_existing_row(slack_db, poll_channel):
    create = SlackMessageCreate.from_slack_message_json(_event(poll_channel.slack_id))
    original = upsert_message(create)
    edited = SlackMessageCreate.from_slack_message_json(
        _event(poll_channel.slack_id, text="hello again")
    )
    upsert_message(edited)

    rows = _stored(slack_db)
    assert len(rows) == 1
    assert rows[0].id == original.id
    assert rows[0].text == "hello again"
