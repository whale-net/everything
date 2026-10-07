"""/shitpost suggest: gate order, rate limit, guardrails, storage, expiry sweep (SQLite)."""

import asyncio
import datetime
from unittest.mock import Mock

import pytest
from temporalio.testing import WorkflowEnvironment
from temporalio.worker import Worker
from temporalio.worker.workflow_sandbox import (
    SandboxedWorkflowRunner,
    SandboxRestrictions,
)
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

from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter as handler,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    shitposter_suggest as suggest,
)
from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    shitposter_dal as dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base  # noqa: E402
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (  # noqa: E402
    ShitposterChannelOptIn,
    ShitposterKillSwitch,
    ShitposterPersona,
    ShitposterSuggestion,
    ShitposterSuggestionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
    SlackCommand,
    SlackUser,
)

ADMINS = frozenset({"U_ADMIN"})
CHAN = "C_ONE"
TEAM = "T1"
NOW = datetime.datetime.now(datetime.UTC)


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
            SlackUser.__table__,
            SlackCommand.__table__,
            ShitposterChannelOptIn.__table__,
            ShitposterKillSwitch.__table__,
            ShitposterPersona.__table__,
            ShitposterSuggestion.__table__,
        ],
    )
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    with Session(engine) as s:
        yield s


@pytest.fixture
def env(session, monkeypatch):
    post = Mock(return_value="1700000000.000100")
    monkeypatch.setattr(suggest, "slack_post_thread_message", post)
    monkeypatch.setattr(suggest, "_member_names", Mock(return_value=["Alice"]))
    monkeypatch.delenv("FCM_SHITPOSTER_SUGGESTION_DAILY_LIMIT", raising=False)
    monkeypatch.delenv("FCM_SHITPOSTER_SUGGESTION_EXPIRY_DAYS", raising=False)
    session.add(ShitposterPersona(name="shitposter"))
    session.commit()
    dal.set_channel_opt_in(CHAN, True, "U_ADMIN", TEAM, session=session)
    return Mock(post=post)


def _suggest(text="be more cats", user="U_S"):
    return handler.handle_shitpost(
        {"user_id": user, "channel_id": CHAN, "text": f"suggest {text}", "team_id": TEAM},
        ADMINS,
        Mock(),
    )


def _rows(session):
    return session.exec(select(ShitposterSuggestion)).all()


def _store(session, submitted_at, user="U_S", status="pending"):
    persona = session.exec(select(ShitposterPersona)).first()
    channel = session.exec(select(SlackChannel)).first()
    row = dal.create_suggestion(
        persona_id=persona.id,
        slack_channel_id=channel.id,
        slack_message_ts=str(submitted_at.timestamp()) + user + status,
        submitter_slack_user_id=user,
        text="old",
        expires_at=submitted_at + datetime.timedelta(days=7),
        submitted_at=submitted_at,
        session=session,
    )
    if status != "pending":
        row.status = status
        session.add(row)
        session.commit()
    return row


def test_happy_path_posts_pending_and_stores_row(env, session):
    reply, notice = _suggest("be more cats")
    assert reply == suggest.ACCEPTED and notice is None
    env.post.assert_called_once()
    assert env.post.call_args.args[0] == CHAN
    assert "be more cats" in env.post.call_args.args[1]
    (row,) = _rows(session)
    assert row.status == ShitposterSuggestionStatusEnum.PENDING.value
    assert row.slack_message_ts == "1700000000.000100"
    assert row.submitter_slack_user_id == "U_S"
    assert row.text == "be more cats"
    delta = row.expires_at - row.submitted_at
    assert datetime.timedelta(days=7) - delta < datetime.timedelta(seconds=5)


def test_not_opted_in_names_opt_in_and_stores_nothing(env, session):
    dal.set_channel_opt_in(CHAN, False, "U_ADMIN", TEAM, session=session)
    reply, _ = _suggest()
    assert reply == suggest.NOT_OPTED_IN
    env.post.assert_not_called()
    assert _rows(session) == []


def test_kill_switch_refuses_and_stores_nothing(env, session):
    dal.set_kill_switch(False, "U_ADMIN", "test", session=session)
    reply, _ = _suggest()
    assert reply == suggest.SILENCED
    env.post.assert_not_called()
    assert _rows(session) == []


def test_opt_in_checked_before_kill_switch(env, session):
    dal.set_channel_opt_in(CHAN, False, "U_ADMIN", TEAM, session=session)
    dal.set_kill_switch(False, "U_ADMIN", "test", session=session)
    reply, _ = _suggest()
    assert reply == suggest.NOT_OPTED_IN


def test_fourth_submission_in_24h_refused_with_retry_time(env, session):
    oldest = NOW - datetime.timedelta(hours=10)
    _store(session, oldest)
    _store(session, NOW - datetime.timedelta(hours=5))
    _store(session, NOW - datetime.timedelta(hours=1))
    reply, _ = _suggest()
    expected_time = (oldest + datetime.timedelta(hours=24)).strftime("%Y-%m-%d %H:%M")
    assert "3 suggestions in the last 24 hours" in reply
    assert expected_time in reply and "UTC" in reply
    env.post.assert_not_called()
    assert len(_rows(session)) == 3


def test_rate_limit_counts_only_trailing_24h(env, session):
    _store(session, NOW - datetime.timedelta(hours=25))
    _store(session, NOW - datetime.timedelta(hours=26))
    _store(session, NOW - datetime.timedelta(hours=27))
    reply, _ = _suggest()
    assert reply == suggest.ACCEPTED


def test_rate_limit_is_per_submitter(env, session):
    for h in (1, 2, 3):
        _store(session, NOW - datetime.timedelta(hours=h), user="U_OTHER")
    reply, _ = _suggest(user="U_S")
    assert reply == suggest.ACCEPTED


def test_limit_is_configurable(env, session, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_SUGGESTION_DAILY_LIMIT", "1")
    _store(session, NOW - datetime.timedelta(hours=1))
    reply, _ = _suggest()
    assert "1 suggestions" in reply


def test_expiry_days_configurable(env, session, monkeypatch):
    monkeypatch.setenv("FCM_SHITPOSTER_SUGGESTION_EXPIRY_DAYS", "2")
    _suggest()
    (row,) = _rows(session)
    delta = row.expires_at - row.submitted_at
    assert datetime.timedelta(days=2) - delta < datetime.timedelta(seconds=5)


@pytest.mark.parametrize(
    "text,expected",
    [
        ("hey <@U999> look", suggest.REFUSAL_TEXT["mention"]),
        ("<!here> ping", suggest.REFUSAL_TEXT["mention"]),
        ("Alice is great", suggest.REFUSAL_TEXT["member_name"]),
        ("kys", suggest.REFUSAL_TEXT["content_filter"]),
    ],
)
def test_guardrail_refusals_store_nothing(env, session, text, expected):
    reply, _ = _suggest(text)
    assert reply == expected
    env.post.assert_not_called()
    assert _rows(session) == []


def test_empty_text_shows_usage(env, session):
    reply, _ = handler.handle_shitpost(
        {"user_id": "U_S", "channel_id": CHAN, "text": "suggest   ", "team_id": TEAM},
        ADMINS,
        Mock(),
    )
    assert reply == suggest.USAGE
    env.post.assert_not_called()


def test_post_failure_stores_nothing(env, session):
    env.post.side_effect = RuntimeError("slack down")
    reply, _ = _suggest()
    assert reply == suggest.POST_FAILED
    assert _rows(session) == []


def test_silence_during_checks_blocks_post(env, session, monkeypatch):
    real = suggest.guardrails.output_refusal

    def silence_then_check(text, names):
        dal.set_kill_switch(False, "U_ADMIN", "race", session=session)
        return real(text, names)

    monkeypatch.setattr(suggest.guardrails, "output_refusal", silence_then_check)
    reply, _ = _suggest()
    assert reply == suggest.SILENCED
    env.post.assert_not_called()
    assert _rows(session) == []


def test_sweep_expires_only_past_due_pending(env, session):
    past = _store(session, NOW - datetime.timedelta(days=8), user="U_A")
    past.expires_at = NOW - datetime.timedelta(hours=1)
    session.add(past)
    session.commit()
    future = _store(session, NOW - datetime.timedelta(days=1), user="U_B")
    promoted = _store(
        session, NOW - datetime.timedelta(days=8), user="U_C", status="promoted"
    )
    promoted.expires_at = NOW - datetime.timedelta(hours=1)
    session.add(promoted)
    session.commit()

    assert suggest.sweep_expired_suggestions(NOW) == 1
    session.expire_all()
    statuses = {r.submitter_slack_user_id: r.status for r in _rows(session)}
    assert statuses == {"U_A": "expired", "U_B": "pending", "U_C": "promoted"}


def test_routes_suggest_without_starting_summon(env, session, monkeypatch):
    start = Mock()
    monkeypatch.setattr(handler, "start_workflow", start)
    _suggest()
    start.assert_not_called()


SWEEP_QUEUE = "fcm-sweep-test"


def test_sweep_workflow_is_registered_with_worker(monkeypatch):
    from friendly_computing_machine.src.friendly_computing_machine.temporal import (
        base,
        worker,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
        activity as shitposter_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.suggestion_sweep_workflow import (
        SWEEP_INTERVAL,
        ShitposterSuggestionSweepWorkflow,
    )

    assert ShitposterSuggestionSweepWorkflow in worker.WORKFLOWS
    assert shitposter_activity.expire_pending_suggestions_activity in worker.ACTIVITIES
    assert issubclass(ShitposterSuggestionSweepWorkflow, base.AbstractScheduleWorkflow)

    monkeypatch.setattr(base, "get_temporal_queue_name", lambda _name: SWEEP_QUEUE)
    schedule = ShitposterSuggestionSweepWorkflow().get_schedule("test")
    assert schedule.spec.intervals[0].every == SWEEP_INTERVAL
    assert schedule.action.task_queue == SWEEP_QUEUE
    assert schedule.action.id == "fcm-test-ShitposterSuggestionSweepWorkflow"


def test_periodic_schedule_action_expires_stale_pending_without_submission(
    env, session, monkeypatch
):
    # the test server has no CreateSchedule, so this runs the schedule's own action
    # (workflow + task queue from get_schedule) and checks the sweep it performs
    from friendly_computing_machine.src.friendly_computing_machine.temporal import base
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter import (
        activity as shitposter_activity,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.suggestion_sweep_workflow import (
        ShitposterSuggestionSweepWorkflow,
    )

    monkeypatch.setattr(base, "get_temporal_queue_name", lambda _name: SWEEP_QUEUE)
    schedule = ShitposterSuggestionSweepWorkflow().get_schedule("test")
    stale = _store(session, NOW - datetime.timedelta(days=30), user="U_OLD")
    fresh = _store(session, NOW, user="U_NEW")
    stale_id, fresh_id = stale.id, fresh.id
    session.commit()

    async def scenario():
        async with await WorkflowEnvironment.start_time_skipping() as wfe:
            async with Worker(
                wfe.client,
                task_queue=SWEEP_QUEUE,
                workflows=[ShitposterSuggestionSweepWorkflow],
                activities=[shitposter_activity.expire_pending_suggestions_activity],
                workflow_runner=SandboxedWorkflowRunner(
                    restrictions=SandboxRestrictions.default.with_passthrough_all_modules()
                ),
            ):
                return await wfe.client.execute_workflow(
                    schedule.action.workflow,
                    id="sweep-run-1",
                    task_queue=schedule.action.task_queue,
                )

    expired_count = asyncio.run(scenario())
    assert expired_count == 1
    session.expire_all()
    statuses = {r.id: r.status for r in _rows(session)}
    assert statuses[stale_id] == "expired"
    assert statuses[fresh_id] == "pending"
