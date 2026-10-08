"""Lore promotion: popular finalized bot posts become `hit` lore entries once.

Runs promote_new_lore directly against an in-memory SQLite DB. Covers the top-share
boundary, negative-dominant and named-member exclusions, operator-retired posts,
lore persisting after the post leaves the window, and idempotent reruns.
"""

import datetime

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterChannelOptIn,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
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
    lore_promotion,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.lore_promotion import (
    cutoff_rank_count,
    popularity_score,
    promote_new_lore,
)
from friendly_computing_machine.src.friendly_computing_machine.util import ts_to_datetime

CHANNEL_SLACK_ID = "C1"
RUN_ID = 1
NOW = datetime.datetime(2026, 10, 7, 12, 0, tzinfo=datetime.timezone.utc)
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
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterMemoryChange.__table__,
]


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
    return engine


@pytest.fixture
def persona(engine):
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
        s.add(SlackUser(slack_id="B_BOT", name="shitposter-bot", is_bot=True))
        s.add(SlackUser(slack_id="U_ALICE", name="alice", is_bot=False))
        s.add(
            ShitposterChannelOptIn(
                slack_channel_id=channel.id,
                opted_in=True,
                set_by_slack_user_id="U_ADMIN",
            )
        )
        s.commit()
        return {"persona_id": persona.id, "revision_id": revision.id, "channel_id": channel.id}


def _add_post(
    engine,
    ids,
    idx: int,
    *,
    text: str,
    reactors: int,
    repliers: int = 0,
    negative: int = 0,
    other: int = 0,
    finalized_days_ago: float = 1.0,
) -> int:
    """Insert a finalized bot post with its message text and engagement row."""
    ts = f"{1_700_000_000 + idx}.000000"
    with Session(engine) as s:
        post = ShitposterPost(
            slack_channel_id=ids["channel_id"],
            slack_message_ts=ts,
            persona_id=ids["persona_id"],
            persona_revision_id=ids["revision_id"],
            trigger="scheduled",
            principal_iss="iss",
            principal_sub="sub",
            principal_kind="service",
            whagent_session_id="sess",
        )
        s.add(post)
        s.flush()
        s.add(
            SlackMessage(
                slack_team_slack_id="T1",
                slack_channel_slack_id=CHANNEL_SLACK_ID,
                slack_user_slack_id="B_BOT",
                text=text,
                ts=ts_to_datetime(ts),
                thread_ts=None,
                parent_user_slack_id=None,
            )
        )
        emoji = {"thumbsdown": negative, "tada": other} if (negative or other) else {}
        s.add(
            ShitposterPostEngagement(
                post_id=post.id,
                persona_id=ids["persona_id"],
                finalized_at=NOW - datetime.timedelta(days=finalized_days_ago),
                distinct_reactors=reactors,
                reactions_by_emoji=emoji,
                distinct_repliers=repliers,
                negative_reactions=negative,
            )
        )
        s.commit()
        return post.id


def _promote(engine, persona_id: int, now: datetime.datetime = NOW, **kwargs):
    with Session(engine) as s:
        outcome = promote_new_lore(s, RUN_ID, persona_id, now, **kwargs)
        s.commit()
        return outcome


def _lore_sources(engine, persona_id: int) -> set[int]:
    return {row.source_post_id for row in dal.active_lore(persona_id)}


def test_cutoff_rank_count_boundaries():
    assert cutoff_rank_count(0, 0.10) == 0
    assert cutoff_rank_count(1, 0.10) == 1
    assert cutoff_rank_count(10, 0.10) == 1
    assert cutoff_rank_count(11, 0.10) == 2
    assert cutoff_rank_count(30, 0.10) == 3
    assert cutoff_rank_count(20, 1.0) == 20


def test_popularity_score_is_reactors_plus_repliers():
    assert popularity_score(4, 3) == 7


def test_top_ten_percent_boundary(engine, persona):
    # 20 posts with popularity 1..20: top 10% is the two most popular
    ids = [
        _add_post(engine, persona, i, text=f"joke {i}", reactors=i)
        for i in range(1, 21)
    ]
    outcome = _promote(engine, persona["persona_id"])
    assert outcome.details["promoted"] == 2
    assert _lore_sources(engine, persona["persona_id"]) == {ids[19], ids[18]}


def test_negative_dominant_post_is_not_promoted(engine, persona):
    negative_top = _add_post(
        engine, persona, 1, text="bad take", reactors=5, negative=4, other=1
    )
    ok = _add_post(engine, persona, 2, text="good take", reactors=3)
    outcome = _promote(engine, persona["persona_id"], top_share=1.0)
    assert _lore_sources(engine, persona["persona_id"]) == {ok}
    assert outcome.details["excluded"]["negative_dominant"] == 1
    assert negative_top not in _lore_sources(engine, persona["persona_id"])


def test_negative_tie_with_other_reactions_is_promoted(engine, persona):
    tied = _add_post(engine, persona, 1, text="split reaction", reactors=2, negative=1, other=1)
    _promote(engine, persona["persona_id"])
    assert _lore_sources(engine, persona["persona_id"]) == {tied}


def test_named_member_post_is_not_promoted(engine, persona):
    named = _add_post(engine, persona, 1, text="alice is the best", reactors=9)
    ok = _add_post(engine, persona, 2, text="nobody here", reactors=3)
    outcome = _promote(engine, persona["persona_id"], top_share=1.0)
    assert _lore_sources(engine, persona["persona_id"]) == {ok}
    assert outcome.details["excluded"]["named_member"] == 1
    assert named not in _lore_sources(engine, persona["persona_id"])


def test_operator_retired_post_is_not_re_promoted(engine, persona):
    post = _add_post(engine, persona, 1, text="old favourite", reactors=9)
    with Session(engine) as s:
        s.add(
            ShitposterLoreEntry(
                persona_id=persona["persona_id"],
                text="old favourite",
                kind="hit",
                source_post_id=post,
                popularity_score=9,
                retired_by_operator=True,
                valid_from=NOW - datetime.timedelta(days=5),
                valid_to=NOW - datetime.timedelta(days=2),
            )
        )
        s.commit()
    outcome = _promote(engine, persona["persona_id"])
    assert outcome.details["promoted"] == 0
    assert outcome.details["excluded"]["operator_retired"] == 1
    assert dal.active_lore(persona["persona_id"]) == []


def test_lore_persists_after_post_leaves_window(engine, persona):
    post = _add_post(engine, persona, 1, text="running joke", reactors=5)
    _promote(engine, persona["persona_id"])
    later = NOW + datetime.timedelta(days=40)
    _promote(engine, persona["persona_id"], now=later)
    active = dal.active_lore(persona["persona_id"])
    assert [row.source_post_id for row in active] == [post]
    assert active[0].valid_to is None
    assert active[0].popularity_score == 5


def test_window_excludes_old_posts_from_ranking(engine, persona):
    # 10 old, very popular posts (already consumed) must not push a new post out of the top share
    for i in range(10):
        _add_post(engine, persona, 100 + i, text=f"old {i}", reactors=100, finalized_days_ago=40)
    with Session(engine) as s:
        s.execute(
            ShitposterPostEngagement.__table__.update().values(consumed_by_lore_run_id=RUN_ID)
        )
        s.commit()
    new = _add_post(engine, persona, 1, text="fresh", reactors=1)
    outcome = _promote(engine, persona["persona_id"])
    assert outcome.details["promoted"] == 1
    assert _lore_sources(engine, persona["persona_id"]) == {new}


def test_rerun_is_idempotent(engine, persona):
    _add_post(engine, persona, 1, text="one", reactors=5)
    _add_post(engine, persona, 2, text="two", reactors=1)
    first = _promote(engine, persona["persona_id"], top_share=0.5)
    history_before = len(dal.change_history(persona["persona_id"]))
    second = _promote(engine, persona["persona_id"], top_share=0.5)
    assert first.details["promoted"] == 1
    assert second.details["consumed"] == 0
    assert second.details["promoted"] == 0
    assert len(dal.active_lore(persona["persona_id"])) == 1
    assert len(dal.change_history(persona["persona_id"])) == history_before


def test_promoted_lore_logs_post_engagement_cause(engine, persona):
    post = _add_post(engine, persona, 1, text="cause check", reactors=2, repliers=1)
    _promote(engine, persona["persona_id"])
    with Session(engine) as s:
        change = s.exec(
            select(ShitposterMemoryChange).where(ShitposterMemoryChange.operation == "add")
        ).one()
        assert change.cause_kind == "post_engagement"
        assert change.cause_post_id == post
        lore = s.exec(select(ShitposterLoreEntry)).one()
        assert lore.popularity_score == 3
        assert lore.kind == "hit"


def test_top_share_and_window_come_from_config(monkeypatch, engine, persona):
    monkeypatch.setenv("FCM_SHITPOSTER_LORE_TOP_SHARE", "0.5")
    monkeypatch.setenv("FCM_SHITPOSTER_LORE_WINDOW_DAYS", "7")
    from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
        load_lore_top_share,
        load_lore_window_days,
    )

    assert load_lore_top_share() == 0.5
    assert load_lore_window_days() == 7
    monkeypatch.delenv("FCM_SHITPOSTER_LORE_TOP_SHARE")
    monkeypatch.delenv("FCM_SHITPOSTER_LORE_WINDOW_DAYS")
    assert load_lore_top_share() == 0.10
    assert load_lore_window_days() == 30
    assert lore_promotion.DEFAULT_TOP_SHARE == 0.10
