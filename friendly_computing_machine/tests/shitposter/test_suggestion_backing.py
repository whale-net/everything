"""Suggestion backing: distinct-backer promotion, outcome replies held by the kill switch.

Red-proof. Each mutation was applied, run, and reverted:

  * is_bot check dropped                 -> bot-reaction threshold test
  * submitter exclusion dropped          -> submitter-counted-once test
  * removed-backer filter dropped        -> removed-reaction test
  * kill-switch check dropped from drain -> held-while-killed test
  * backer names rendered into reply     -> reply-text tests
"""

import asyncio
import datetime
from unittest.mock import Mock

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.bot import app as app_mod


class _FakeApp:
    """Stand-in for slack_bolt App: every decorator is a passthrough."""

    def __getattr__(self, _name):
        def _maybe(*a, **k):
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_reaction_dal import (  # noqa: E402
    add_reaction,
    remove_reaction,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base  # noqa: E402
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (  # noqa: E402
    ShitposterKillSwitch,
    ShitposterPersona,
    ShitposterSuggestion,
    ShitposterSuggestionBacker,
    ShitposterSuggestionReplyOutbox,
    ShitposterSuggestionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
    SlackMessage,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack_reaction import (  # noqa: E402
    SlackReaction,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (  # noqa: E402
    suggestion_backer_threshold,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (  # noqa: E402
    activity as shitposter_activity,
)

CHAN = "C_ONE"
MSG_TS = "1700000000.000100"
SUBMITTER = "U_SUBMITTER"
SUGGESTION_TEXT = "secret declined phrase xyz"
NOW = datetime.datetime.now(datetime.UTC)
THRESHOLD = 3


@pytest.fixture
def session(monkeypatch):
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(
        engine,
        tables=[
            SlackChannel.__table__,
            SlackMessage.__table__,
            ShitposterKillSwitch.__table__,
            ShitposterPersona.__table__,
            ShitposterSuggestion.__table__,
            ShitposterSuggestionBacker.__table__,
            ShitposterSuggestionReplyOutbox.__table__,
            SlackReaction.__table__,
        ],
    )
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    monkeypatch.delenv("FCM_SHITPOSTER_SUGGESTION_BACKER_THRESHOLD", raising=False)
    with Session(engine) as s:
        yield s


@pytest.fixture
def post(monkeypatch):
    mock = Mock(return_value="1700000001.000100")
    monkeypatch.setattr(shitposter_activity, "slack_post_thread_message", mock)
    return mock


@pytest.fixture
def suggestion(session):
    persona = ShitposterPersona(name="shitposter")
    channel = SlackChannel(slack_id=CHAN, name="general", channel_type="channel")
    session.add_all([persona, channel])
    session.commit()
    row = dal.create_suggestion(
        persona_id=persona.id,
        slack_channel_id=channel.id,
        slack_message_ts=MSG_TS,
        submitter_slack_user_id=SUBMITTER,
        text=SUGGESTION_TEXT,
        expires_at=NOW + datetime.timedelta(days=7),
        submitted_at=NOW,
        session=session,
    )
    return row.id


def _status(session, suggestion_id):
    session.expire_all()
    return session.get(ShitposterSuggestion, suggestion_id)


def _react(user, *, is_bot=False, emoji="thumbsup", threshold=THRESHOLD):
    add_reaction(CHAN, MSG_TS, user, emoji, is_bot=is_bot)
    return dal.on_suggestion_reaction_added(CHAN, MSG_TS, user, is_bot, threshold)


def _unreact(user, emoji="thumbsup"):
    remove_reaction(CHAN, MSG_TS, user, emoji)
    dal.on_suggestion_reaction_removed(CHAN, MSG_TS, user)


def _drain():
    return asyncio.run(shitposter_activity.drain_suggestion_replies_activity())


def _set_kill_switch(session, enabled):
    dal.set_kill_switch(enabled, "U_ADMIN", "test", session=session)


# --- threshold -------------------------------------------------------------


def test_promotes_on_third_distinct_human_including_submitter(session, suggestion):
    assert _react("U_A") is False
    assert _status(session, suggestion).status == "pending"
    assert _react("U_B") is True
    row = _status(session, suggestion)
    assert row.status == ShitposterSuggestionStatusEnum.PROMOTED.value
    assert row.promoted_at is not None
    assert row.consumed_by_reflector_run_id is None


def test_below_threshold_never_promotes_or_queues_reply(session, suggestion):
    _react("U_A")
    row = _status(session, suggestion)
    assert row.status == "pending"
    assert row.promoted_at is None
    assert session.exec(select(ShitposterSuggestionReplyOutbox)).all() == []


def test_duplicate_reactions_from_one_user_count_once(session, suggestion):
    _react("U_A")
    _react("U_A")
    _react("U_A", emoji="tada")
    assert _status(session, suggestion).status == "pending"
    backers = session.exec(select(ShitposterSuggestionBacker)).all()
    assert [b.slack_user_id for b in backers] == ["U_A"]


def test_bot_and_unknown_bot_reactions_are_not_counted(session, suggestion):
    assert _react("U_BOT", is_bot=True) is False
    assert _react("U_UNKNOWN", is_bot=None) is False
    _react("U_A")
    assert _status(session, suggestion).status == "pending"
    assert session.exec(select(ShitposterSuggestionBacker)).all()[0].slack_user_id == "U_A"


def test_removed_reaction_releases_backer(session, suggestion):
    # threshold 4: S + A + B = 3, B removes -> 2, C -> 3 (still pending), D -> 4
    _react("U_A", threshold=4)
    _react("U_B", threshold=4)
    _unreact("U_B")
    assert _status(session, suggestion).status == "pending"
    assert _react("U_C", threshold=4) is False
    assert _status(session, suggestion).status == "pending"
    assert _react("U_D", threshold=4) is True
    assert _status(session, suggestion).status == "promoted"


def test_removal_of_one_emoji_keeps_backer_while_another_remains(session, suggestion):
    _react("U_A", emoji="thumbsup")
    _react("U_A", emoji="tada")
    _unreact("U_A", emoji="thumbsup")
    assert _status(session, suggestion).status == "pending"
    backer = session.exec(select(ShitposterSuggestionBacker)).one()
    assert backer.removed_at is None
    assert _react("U_B") is True


def test_submitter_reaction_is_not_a_second_backer(session, suggestion):
    assert _react(SUBMITTER) is False
    assert session.exec(select(ShitposterSuggestionBacker)).all() == []
    _react("U_A")
    # submitter + U_A is two, so the reaction from the submitter must not reach three
    assert _status(session, suggestion).status == "pending"
    assert _react("U_B") is True


def test_promoted_suggestion_ignores_later_reactions(session, suggestion):
    _react("U_A")
    _react("U_B")
    assert _react("U_C") is False
    assert _status(session, suggestion).status == "promoted"


def test_threshold_is_read_from_env(monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_SUGGESTION_BACKER_THRESHOLD", "5")
    assert suggestion_backer_threshold() == 5
    monkeypatch.delenv("FCM_SHITPOSTER_SUGGESTION_BACKER_THRESHOLD")
    assert suggestion_backer_threshold() == 3


# --- outcome queue ----------------------------------------------------------


def test_outcome_on_pending_suggestion_is_refused(session, suggestion):
    assert dal.enqueue_suggestion_outcome(suggestion, "applied") is False
    assert session.exec(select(ShitposterSuggestionReplyOutbox)).all() == []


def test_declined_outcome_requires_valid_coarse_reason(session, suggestion):
    _react("U_A")
    _react("U_B")
    with pytest.raises(ValueError):
        dal.enqueue_suggestion_outcome(suggestion, "declined", None)
    with pytest.raises(ValueError):
        dal.enqueue_suggestion_outcome(suggestion, "declined", "because I said so")
    assert _status(session, suggestion).status == "promoted"


def test_outcome_queued_once_per_promoted_suggestion(session, suggestion):
    _react("U_A")
    _react("U_B")
    assert dal.enqueue_suggestion_outcome(suggestion, "declined", "off_topic") is True
    assert dal.enqueue_suggestion_outcome(suggestion, "applied") is False
    rows = session.exec(select(ShitposterSuggestionReplyOutbox)).all()
    assert [(r.kind, r.coarse_reason) for r in rows] == [("declined", "off_topic")]
    assert _status(session, suggestion).status == "declined"


# --- kill switch and reply text ---------------------------------------------


def test_reply_held_while_killed_then_posted_after_release(session, suggestion, post):
    _react("U_A")
    _react("U_B")
    dal.enqueue_suggestion_outcome(suggestion, "applied")

    _set_kill_switch(session, False)
    assert _drain() == 0
    post.assert_not_called()
    assert session.exec(select(ShitposterSuggestionReplyOutbox)).one().posted_at is None

    _set_kill_switch(session, True)
    assert _drain() == 1
    post.assert_called_once()
    assert post.call_args.args[0] == CHAN
    assert post.call_args.kwargs["thread_ts"] == MSG_TS
    session.expire_all()
    assert session.exec(select(ShitposterSuggestionReplyOutbox)).one().posted_at is not None

    assert _drain() == 0
    assert post.call_count == 1


def test_expired_reply_is_queued_and_posted(session, suggestion, post):
    row = session.get(ShitposterSuggestion, suggestion)
    row.expires_at = NOW - datetime.timedelta(hours=1)
    session.add(row)
    session.commit()
    assert dal.expire_pending_suggestions(NOW, session=session) == 1
    assert _drain() == 1
    text = post.call_args.args[1]
    assert text == shitposter_activity.render_suggestion_reply("expired", None)


@pytest.mark.parametrize(
    "kind,reason",
    [
        ("applied", None),
        ("declined", "off_topic"),
        ("declined", "unsafe"),
        ("declined", "duplicate"),
        ("declined", "other"),
        ("expired", None),
    ],
)
def test_reply_text_never_names_backers_or_quotes_text(kind, reason):
    text = shitposter_activity.render_suggestion_reply(kind, reason)
    for forbidden in (
        SUBMITTER,
        "U_A",
        "U_B",
        "U_BOT",
        "<@",
        "<!",
        "secret",
        "declined phrase",
        "xyz",
        '"',
        "“",
    ):
        assert forbidden not in text


def test_posted_reply_text_has_no_backer_names_or_suggestion_text(
    session, suggestion, post
):
    _react("U_A")
    _react("U_B")
    dal.enqueue_suggestion_outcome(suggestion, "declined", "unsafe")
    assert _drain() == 1
    posted = post.call_args.args[1]
    assert "U_A" not in posted and "U_B" not in posted and SUBMITTER not in posted
    assert SUGGESTION_TEXT not in posted
