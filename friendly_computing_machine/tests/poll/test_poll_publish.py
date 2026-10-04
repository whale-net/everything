"""Characterization of the bolt-free /wpoll publish path (krill M5).

The create -> post -> set-ts path lives in poll/publish.py so the
Temporal music-poll worker can publish a poll without importing the
bolt app. These tests pin that path as /wpoll behaves today: the poll
is stored first, the rendered message is posted, and only a
successful post records the message ts. A failed post leaves the
stored poll without a ts and returns the user-facing invite message.
"""

import sys
from unittest.mock import Mock

import pytest
from slack_sdk.errors import SlackApiError
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    PollSpec,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.render import (
    render_poll_blocks,
    render_poll_text,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    get_poll_snapshot,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    init_engine,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
    PollVote,
)
from friendly_computing_machine.src.friendly_computing_machine.poll.publish import (
    ScheduledPollPost,
    publish_poll,
    publish_scheduled_poll,
)

# /wpoll's own spec, as the command handler parses it.
_SPEC = PollSpec(question="Lunch?", options=["Tacos", "Pizza"])

_POST_ERROR = (
    "I couldn't post the poll here. Invite me to this channel and try again."
)


@pytest.fixture(scope="module")
def engine():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(
        engine, tables=[Poll.__table__, PollOption.__table__, PollVote.__table__]
    )
    # publish_poll runs its DAL calls on the shared engine singleton,
    # so the path under test writes where this fixture reads.
    init_engine(engine)
    return engine


def _client(ts="123.456"):
    client = Mock()
    client.chat_postMessage.return_value = {"channel": "C1", "ts": ts}
    return client


def _latest_poll(engine) -> int:
    with Session(engine) as session:
        poll = session.exec(select(Poll).order_by(Poll.id.desc())).first()
        assert poll is not None, "publish_poll stored no poll row"
        return poll.id


def test_publish_path_imports_without_the_bolt_app():
    # This py_test target is its own process (as test_wai is), and
    # nothing in it imports the bolt app, so slack_bolt's absence
    # after the module-level import of the publish path above proves
    # that path's import closure is bolt-free.
    assert "slack_bolt" not in sys.modules


def test_publish_poll_stores_posts_and_records_ts(engine):
    assert publish_poll(_client(), _SPEC, "C1", "U0") is None

    with Session(engine) as session:
        snap = get_poll_snapshot(_latest_poll(engine), session=session)
        assert snap.poll.slack_message_ts == "123.456"
        assert snap.poll.slack_channel_slack_id == "C1"
        assert snap.poll.creator_slack_user_slack_id == "U0"
        assert [t.option.text for t in snap.options] == ["Tacos", "Pizza"]


def test_publish_poll_posts_the_rendered_poll(engine):
    client = _client()
    publish_poll(client, _SPEC, "C1", "U0")

    with Session(engine) as session:
        snap = get_poll_snapshot(_latest_poll(engine), session=session)
        kwargs = client.chat_postMessage.call_args.kwargs
        assert kwargs["channel"] == "C1"
        assert kwargs["text"] == render_poll_text(snap)
        assert kwargs["blocks"] == render_poll_blocks(snap)


def test_publish_poll_leaves_no_ts_when_the_post_fails(engine):
    client = Mock()
    client.chat_postMessage.side_effect = SlackApiError(
        "not_in_channel", {"error": "not_in_channel"}
    )

    error = publish_poll(client, _SPEC, "C1", "U0")

    assert error == _POST_ERROR
    with Session(engine) as session:
        snap = get_poll_snapshot(_latest_poll(engine), session=session)
        assert snap.poll.slack_message_ts is None


# ----- the scheduled poll's publish path ----------------------


def test_publish_scheduled_poll_marks_the_poll_automated(engine):
    post = publish_scheduled_poll(_client(), _SPEC, "C1", "U_BOT")

    assert isinstance(post, ScheduledPollPost)
    with Session(engine) as session:
        snap = get_poll_snapshot(post.poll_id, session=session)
        assert snap.poll.automated is True
        assert snap.poll.creator_slack_user_slack_id == "U_BOT"
        assert snap.poll.slack_message_ts == post.slack_message_ts


def test_publish_scheduled_poll_returns_the_error_when_the_post_fails(
    engine,
):
    client = Mock()
    client.chat_postMessage.side_effect = SlackApiError(
        "not_in_channel", {"error": "not_in_channel"}
    )

    post = publish_scheduled_poll(client, _SPEC, "C1", "U_BOT")

    assert post == _POST_ERROR
    with Session(engine) as session:
        snap = get_poll_snapshot(_latest_poll(engine), session=session)
        assert snap.poll.slack_message_ts is None
