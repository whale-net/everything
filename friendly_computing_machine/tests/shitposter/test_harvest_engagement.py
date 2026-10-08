"""Harvest brain job finalizes each bot post's engagement record once, 24h after posting.

Runs the harvest compute and apply steps directly against an in-memory SQLite DB.
Covers: removed reactions, bot reactions, the negative-emoji split, posts younger
than 24h, feedback arriving after finalization, and idempotent reruns.
"""

import datetime

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
    ShitposterBrainJobTrigger,
    ShitposterChannelOptIn,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (
    SlackReaction,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    harvest,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BrainJobInput,
)
from friendly_computing_machine.src.friendly_computing_machine.util import ts_to_datetime

CHANNEL_SLACK_ID = "C1"
TABLES = [
    SlackUser.__table__,
    SlackChannel.__table__,
    SlackMessage.__table__,
    SlackReaction.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterPost.__table__,
    ShitposterPostEngagement.__table__,
]


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _ago(hours: float) -> datetime.datetime:
    return _now() - datetime.timedelta(hours=hours)


def _ts(dt: datetime.datetime) -> str:
    return f"{dt.timestamp():.6f}"


@pytest.fixture
def engine(monkeypatch):
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    monkeypatch.setenv("FCM_SHITPOSTER_NEGATIVE_EMOJI", "thumbsdown,:-1:")
    return engine


@pytest.fixture
def seeded(engine):
    """One persona, one opted-in channel, and a bot user; returns the ids."""
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.flush()
        revision = ShitposterPersonaRevision(persona_id=persona.id, persona_text="seed")
        s.add(revision)
        s.flush()
        channel = SlackChannel(
            id=1, slack_id=CHANNEL_SLACK_ID, name="general", channel_type="channel"
        )
        s.add(channel)
        s.add(
            SlackUser(slack_id="B_BOT", name="shitposter-bot", is_bot=True)
        )
        s.add(
            ShitposterChannelOptIn(
                slack_channel_id=channel.id,
                opted_in=True,
                set_by_slack_user_id="U_ADMIN",
            )
        )
        s.commit()
        return {"persona_id": persona.id, "revision_id": revision.id, "channel_id": channel.id}


def _add_post(engine, ids, created_at, channel_id=None) -> tuple[int, str]:
    """Insert a bot post; returns (post id, slack ts)."""
    ts = _ts(created_at)
    with Session(engine) as s:
        post = ShitposterPost(
            slack_channel_id=channel_id or ids["channel_id"],
            slack_message_ts=ts,
            persona_id=ids["persona_id"],
            persona_revision_id=ids["revision_id"],
            trigger="scheduled",
            principal_iss="iss",
            principal_sub="sub",
            principal_kind="service",
            whagent_session_id="sess",
            created_at=created_at,
        )
        s.add(post)
        s.commit()
        return post.id, ts


def _react(engine, ts, user, emoji, added, removed=None, is_bot=None):
    with Session(engine) as s:
        s.add(
            SlackReaction(
                slack_channel_slack_id=CHANNEL_SLACK_ID,
                message_ts=ts,
                slack_user_slack_id=user,
                emoji=emoji,
                added_at=added,
                removed_at=removed,
                is_bot=is_bot,
            )
        )
        s.commit()


def _reply(engine, ts, user):
    with Session(engine) as s:
        s.add(
            SlackMessage(
                slack_id=None,
                slack_team_slack_id="T1",
                slack_channel_slack_id=CHANNEL_SLACK_ID,
                slack_user_slack_id=user,
                text="reply",
                ts=_now(),
                thread_ts=ts_to_datetime(ts),
                parent_user_slack_id=None,
                processed_date=None,
                slack_team_id=None,
                slack_channel_id=None,
                slack_user_id=None,
                slack_parent_user_id=None,
            )
        )
        s.commit()


def _params(persona_id) -> BrainJobInput:
    return BrainJobInput(
        persona_id=persona_id,
        job_kind=ShitposterBrainJobKind.HARVEST.value,
        trigger=ShitposterBrainJobTrigger.SCHEDULE.value,
    )


def _run(engine, persona_id):
    """Compute then apply one harvest pass, the way the runner does."""
    payload = harvest._compute(_params(persona_id))
    with Session(engine) as s:
        outcome = harvest._apply(s, run_id=0, payload=payload)
        s.commit()
    return payload, outcome


def _engagements(engine) -> dict[int, ShitposterPostEngagement]:
    with Session(engine) as s:
        return {
            row.post_id: row
            for row in s.exec(select(ShitposterPostEngagement)).all()
        }


def test_removed_reaction_not_counted(engine, seeded):
    post_id, ts = _add_post(engine, seeded, _ago(30))
    _react(engine, ts, "U1", "+1", _ago(29), removed=_ago(28))
    _react(engine, ts, "U2", "+1", _ago(29))
    _reply(engine, ts, "U1")
    _reply(engine, ts, "U2")
    _reply(engine, ts, "B_BOT")

    _run(engine, seeded["persona_id"])

    row = _engagements(engine)[post_id]
    assert row.distinct_reactors == 1
    assert row.reactions_by_emoji == {"+1": 1}
    assert row.distinct_repliers == 2


def test_bot_reaction_not_counted(engine, seeded):
    post_id, ts = _add_post(engine, seeded, _ago(30))
    _react(engine, ts, "B_BOT", "+1", _ago(29))  # bot per SlackUser
    _react(engine, ts, "U9", "fire", _ago(29), is_bot=True)  # bot flagged on the reaction
    _react(engine, ts, "U1", "+1", _ago(29))

    _run(engine, seeded["persona_id"])

    row = _engagements(engine)[post_id]
    assert row.distinct_reactors == 1
    assert row.reactions_by_emoji == {"+1": 1}


def test_negative_split(engine, seeded):
    post_id, ts = _add_post(engine, seeded, _ago(30))
    _react(engine, ts, "U1", "+1", _ago(29))
    _react(engine, ts, "U2", "thumbsdown", _ago(29))
    _react(engine, ts, "U3", "-1", _ago(29))
    _react(engine, ts, "U4", "thumbsdown", _ago(29))
    _react(engine, ts, "U1", "fire", _ago(29))

    _run(engine, seeded["persona_id"])

    row = _engagements(engine)[post_id]
    assert row.distinct_reactors == 4
    assert row.reactions_by_emoji == {"+1": 1, "thumbsdown": 2, "-1": 1, "fire": 1}
    assert row.negative_reactions == 3


def test_unopted_or_under_24h_posts_skipped(engine, seeded):
    with Session(engine) as s:
        other = SlackChannel(id=2, slack_id="C2", name="off", channel_type="channel")
        s.add(other)
        s.add(
            ShitposterChannelOptIn(
                slack_channel_id=other.id, opted_in=False, set_by_slack_user_id="U_ADMIN"
            )
        )
        s.commit()

    young_id, young_ts = _add_post(engine, seeded, _ago(2))
    _react(engine, young_ts, "U1", "+1", _ago(1))
    off_id, off_ts = _add_post(engine, seeded, _ago(30), channel_id=2)
    old_id, old_ts = _add_post(engine, seeded, _ago(30))
    _react(engine, old_ts, "U1", "+1", _ago(29))

    _run(engine, seeded["persona_id"])

    rows = _engagements(engine)
    assert young_id not in rows
    assert off_id not in rows
    assert old_id in rows


def test_late_feedback_does_not_alter_row(engine, seeded):
    post_id, ts = _add_post(engine, seeded, _ago(30))
    _react(engine, ts, "U1", "+1", _ago(29))
    _run(engine, seeded["persona_id"])
    before = _engagements(engine)[post_id]
    finalized_before = before.finalized_at
    counts_before = (before.distinct_reactors, dict(before.reactions_by_emoji))

    _react(engine, ts, "U2", "fire", _now())
    _react(engine, ts, "U3", "thumbsdown", _now())
    _reply(engine, ts, "U4")
    payload, outcome = _run(engine, seeded["persona_id"])

    assert payload["records"] == []
    assert outcome.status == ShitposterBrainJobStatus.NO_OP.value
    after = _engagements(engine)
    assert len(after) == 1
    row = after[post_id]
    assert (row.distinct_reactors, dict(row.reactions_by_emoji)) == counts_before
    assert row.negative_reactions == 0
    assert row.distinct_repliers == 0
    assert row.finalized_at == finalized_before


def test_rerun_is_idempotent(engine, seeded):
    post_id, ts = _add_post(engine, seeded, _ago(30))
    _react(engine, ts, "U1", "+1", _ago(29))

    first_payload, first = _run(engine, seeded["persona_id"])
    assert first.status == ShitposterBrainJobStatus.SUCCEEDED.value
    assert first.details == {"finalized": 1, "already_finalized": 0}
    finalized_at = _engagements(engine)[post_id].finalized_at

    _, second = _run(engine, seeded["persona_id"])
    assert second.status == ShitposterBrainJobStatus.NO_OP.value
    assert second.details == {"finalized": 0, "already_finalized": 0}

    # replaying the first payload must not rewrite or duplicate the row
    with Session(engine) as s:
        replay = harvest._apply(s, run_id=0, payload=first_payload)
        s.commit()
    assert replay.details == {"finalized": 0, "already_finalized": 1}

    rows = _engagements(engine)
    assert len(rows) == 1
    assert rows[post_id].finalized_at == finalized_at
    assert rows[post_id].distinct_reactors == 1
