"""Characterization of the `/wai` slash command (krill M1, FRs e1201623 / 00e4b7f8).

M1 is an ONBOARDING milestone: `/wai` and `/wpoll` must survive the refactor
UNCHANGED. Nothing here asserts how `/wai` *should* work -- every test pins what
it does today on main@5473180b, so a later refactor that changes it turns red.

The `/wai` path, end to end:

    bolt handler (bot/handlers/commands.py)
      -> insert_slack_command          (audit row)
      -> insert_genai_text             (audit row, written BEFORE the workflow)
      -> execute_workflow(SlackContextGeminiWorkflow.run, params(channel_id, text))
           -> get_slack_channel_context(channel_id)   -> list[GenAIText]
           -> generate_summary(genaitexts) / get_vibe(prompt)
           -> generate_context_prompt(prompt, summary, vibe)
           -> generate_gemini_response(context_prompt)
           -> detect_call_to_action(response)
           -> fix_slack_tagging_activity(response, cta)
      -> update_genai_text_response(id, answer)
      -> say(answer)

Only the Gemini call is stubbed. The activities, the DAL, and the handler are
the real ones, against a real in-memory SQLite DB with the `fcm` schema.

Two deliberate deviations from the production path, both documented in place:
the bolt `App` is faked (importing a handler module would build a real App and
make a network auth.test call), and the workflow body is driven by a local
coroutine replaying its activity order -- this repo has no
WorkflowEnvironment/time-skipping harness, the same constraint
tests/test_whagent_workflow.py documents. The handler's choice of
`SlackContextGeminiWorkflow.run` is asserted directly, so the chain is pinned
from both ends.

Red-proof. Each mutation below was applied, run, and reverted; the listed
tests went red, which is what makes the rest of this file load-bearing rather
than vacuous:

  * say() called twice in the handler        -> answers-exactly-once, per-user
  * previous_context dropped from the prompt -> grounding-in-stored-rows,
                                                influenced-by-previous-answer
  * insert_genai_text skipped               -> 12 tests, incl. the audit-row ones
  * get_slack_channel_context returns []     -> the FR 00e4b7f8 xfail XPASSes
                                                and strict-xfail fails it
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
            # direct decorator use: @app.command  -> return fn unchanged
            if len(a) == 1 and not k and callable(a[0]):
                return a[0]

            # factory use: @app.event("x") / @app.action("x") -> passthrough deco
            def deco(fn):
                return fn

            return deco

        return _maybe


app_mod._app_instance = _FakeApp()

# imported after the fake app is installed -- see test_whagent_gating.py
from friendly_computing_machine.src.friendly_computing_machine.bot.handlers import (  # noqa: E402
    commands,
)
from friendly_computing_machine.src.friendly_computing_machine.db import (  # noqa: E402
    util as db_util,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (  # noqa: E402
    get_genai_texts_by_slack_channel,
)
from friendly_computing_machine.src.friendly_computing_machine.models.genai import (  # noqa: E402
    GenAIText,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (  # noqa: E402
    SlackChannel,
    SlackChannelAgentLink,
    SlackCommand,
    SlackMessage,
    SlackThreadSession,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.ai import (  # noqa: E402
    activity as ai_activity,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.activity import (  # noqa: E402
    FixSlackTaggingParams,
    GenerateContextPromptParams,
    fix_slack_tagging_activity,
    generate_context_prompt,
    get_slack_channel_context,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.workflow import (  # noqa: E402
    SlackContextGeminiWorkflow,
    SlackContextGeminiWorkflowParams,
)

CHANNEL = "C_GENERAL"
OTHER_CHANNEL = "C_OTHER"
USER = "U_WHALE"
ANSWER = "A grounded answer about the channel."


@pytest.fixture
def db(monkeypatch):
    """Point the real DAL at an in-memory SQLite DB with the `fcm` schema."""
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    tables = [
        GenAIText.__table__,
        SlackCommand.__table__,
        SlackMessage.__table__,
        SlackChannel.__table__,
        SlackUser.__table__,
        SlackThreadSession.__table__,
        SlackChannelAgentLink.__table__,
    ]
    from friendly_computing_machine.src.friendly_computing_machine.models.base import Base

    Base.metadata.create_all(engine, tables=tables)
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    yield engine


@pytest.fixture
def genai_prompts(monkeypatch):
    """Stub the Gemini call; record every prompt the model was asked to run."""
    seen: list[str] = []

    async def _fake_gen_text(prompt: str) -> str:
        seen.append(prompt)
        if prompt.lstrip().startswith("Here is are the previous genAI requests"):
            return "SUMMARY"
        if "vibe of this prompt" in prompt:
            return "curious"
        if "call to action" in prompt:
            return "NO"
        return ANSWER

    monkeypatch.setattr(ai_activity, "gen_text", _fake_gen_text)
    return seen


async def _drive_workflow(params):
    """Replay SlackContextGeminiWorkflow.run's activity order, in-process.

    The production body calls these through workflow.execute_activity, which
    needs a Temporal worker; everything below is the real activity function.
    """
    slack_prompts = await get_slack_channel_context(params.slack_channel_slack_id)
    summary, vibe = await asyncio.gather(
        ai_activity.generate_summary(slack_prompts),
        ai_activity.get_vibe(params.prompt),
    )
    context_prompt = await generate_context_prompt(
        GenerateContextPromptParams(params.prompt, summary, vibe)
    )
    response = await ai_activity.generate_gemini_response(context_prompt)
    is_call_to_action = await ai_activity.detect_call_to_action(response)
    return await fix_slack_tagging_activity(
        FixSlackTaggingParams(response, is_call_to_action)
    )


@pytest.fixture
def wai_env(monkeypatch, db, genai_prompts):
    """Run the real handler with Temporal and the model stubbed out."""
    calls = []

    def fake_execute_workflow(runner, workflow_args=None, **kwargs):
        calls.append({"runner": runner, "args": workflow_args, "kwargs": kwargs})
        return asyncio.run(_drive_workflow(workflow_args))

    monkeypatch.setattr(commands, "execute_workflow", fake_execute_workflow)
    monkeypatch.setattr(commands, "get_temporal_queue_name", lambda n: f"fcm-test-{n}")

    ack = Mock(name="ack")
    say = Mock(name="say")
    return {"ack": ack, "say": say, "calls": calls, "prompts": genai_prompts}


def _fire(env, text="what did we decide about the boat?", user=USER, channel=CHANNEL):
    commands.handle_whale_ai_command(
        env["ack"],
        env["say"],
        {
            "user_name": "whale",
            "user_id": user,
            "channel_id": channel,
            "text": text,
        },
    )


def _seed_genaitext(engine, prompt, response, channel=CHANNEL, created_at=None):
    with Session(engine) as s:
        s.add(
            GenAIText(
                slack_channel_slack_id=channel,
                slack_user_slack_id=USER,
                prompt=prompt,
                response=response,
                created_at=created_at or datetime.datetime(2026, 1, 1),
            )
        )
        s.commit()


def _seed_slack_message(engine, text, channel=CHANNEL):
    with Session(engine) as s:
        s.add(
            SlackMessage(
                slack_id="m1",
                slack_team_slack_id="T1",
                slack_channel_slack_id=channel,
                slack_user_slack_id=USER,
                text=text,
                ts=datetime.datetime(2026, 1, 1),
                thread_ts=None,
                parent_user_slack_id=None,
            )
        )
        s.commit()


def _genaitexts(engine, channel=CHANNEL):
    with Session(engine) as s:
        return list(s.exec(select(GenAIText)).all())


def _summary_prompts(prompts):
    """The prompt(s) generate_summary sent to the model, oldest invocation first."""
    return [
        p
        for p in prompts
        if p.lstrip().startswith("Here is are the previous genAI requests")
    ]


def _answer_prompts(prompts):
    """The prompt(s) generate_gemini_response sent to the model."""
    return [p for p in prompts if "## User Prompt" in p]


# ---------------------------------------------------------------- e1201623


def test_wai_answers_exactly_once_and_acks(wai_env, db):
    _fire(wai_env)

    wai_env["ack"].assert_called_once()
    # exactly one say() -- the answer, once, and nothing else
    wai_env["say"].assert_called_once()
    assert wai_env["say"].call_args.kwargs["text"] == ANSWER


def test_wai_runs_the_slack_context_gemini_workflow_for_that_channel(wai_env, db):
    _fire(wai_env, text="what is up", channel=CHANNEL)

    assert len(wai_env["calls"]) == 1
    call = wai_env["calls"][0]
    assert call["runner"] == SlackContextGeminiWorkflow.run
    assert call["args"] == SlackContextGeminiWorkflowParams(CHANNEL, "what is up")
    assert call["kwargs"]["task_queue"] == "fcm-test-main"
    assert call["kwargs"]["id"].startswith(f"test_id-command-wai-{CHANNEL}-")


def test_wai_writes_a_genaitext_row_for_the_prompt(wai_env, db):
    _fire(wai_env, text="what did we decide about the boat?")

    rows = _genaitexts(db)
    assert len(rows) == 1
    assert rows[0].prompt == "what did we decide about the boat?"
    assert rows[0].slack_channel_slack_id == CHANNEL
    assert rows[0].slack_user_slack_id == USER
    # the answer is stamped onto that same row, and only that row
    assert rows[0].response == ANSWER
    assert rows[0].response_as_of is not None


def test_wai_writes_a_slack_command_row_too(wai_env, db):
    _fire(wai_env, text="what is up")

    with Session(db) as s:
        cmds = list(s.exec(select(SlackCommand)).all())
    assert len(cmds) == 1
    assert cmds[0].command_base == "/wai"
    assert cmds[0].command_text == "what is up"
    assert cmds[0].caller_slack_user_id == USER


def test_wai_answer_is_grounded_in_the_channels_stored_rows(wai_env, db):
    _seed_genaitext(db, "where do we keep the spare oars?", "in the blue locker")
    _seed_genaitext(
        db,
        "when is the regatta?",
        "saturday at noon",
        created_at=datetime.datetime(2026, 1, 2),
    )

    _fire(wai_env, text="remind me about the regatta")

    # the model was shown the channel's earlier prompts and answers
    (summary_prompt,) = _summary_prompts(wai_env["prompts"])
    assert "where do we keep the spare oars?" in summary_prompt
    assert "in the blue locker" in summary_prompt
    assert "when is the regatta?" in summary_prompt
    assert "saturday at noon" in summary_prompt

    # and the answer's own prompt carries that summary plus the new question
    (answer_prompt,) = _answer_prompts(wai_env["prompts"])
    assert "SUMMARY" in answer_prompt
    assert "remind me about the regatta" in answer_prompt
    # a call-to-action answer gets an @here prefix, and @channel is escaped
    assert wai_env["say"].call_args.kwargs["text"] == ANSWER


def test_wai_grounding_is_scoped_to_the_invoking_channel(wai_env, db):
    _seed_genaitext(db, "here?", "here!", channel=CHANNEL)
    _seed_genaitext(db, "secret from another channel", "do not leak",
                    channel=OTHER_CHANNEL)

    _fire(wai_env, channel=CHANNEL)

    (summary_prompt,) = _summary_prompts(wai_env["prompts"])
    assert "here?" in summary_prompt
    assert "secret from another channel" not in summary_prompt
    assert "do not leak" not in summary_prompt


def test_wai_context_caps_at_the_last_ten_rows_for_the_channel(wai_env, db):
    for i in range(14):
        _seed_genaitext(
            db,
            f"old question {i}",
            f"old answer {i}",
            created_at=datetime.datetime(2026, 1, 1) + datetime.timedelta(hours=i),
        )

    _fire(wai_env, text="what now?")

    (summary_prompt,) = _summary_prompts(wai_env["prompts"])
    # the DAL's default limit=10, newest first -- and the invocation's own
    # audit row is inserted first, so it occupies one of the ten slots
    assert "old question 13" in summary_prompt
    assert "old question 5" in summary_prompt
    assert "old question 4" not in summary_prompt
    assert "old question 0" not in summary_prompt


def test_wai_sees_its_own_audit_row_in_the_context_it_builds(wai_env, db):
    # The handler inserts the genaitext row BEFORE running the workflow, so the
    # invocation's own unanswered prompt is part of the context it then builds.
    _fire(wai_env, text="what did we decide about the boat?")

    summary_prompt = next(
        p
        for p in wai_env["prompts"]
        if p.lstrip().startswith("Here is are the previous genAI requests")
    )
    assert "what did we decide about the boat?" in summary_prompt


def test_two_invocations_by_different_users_are_each_answered_once(wai_env, db):
    _fire(wai_env, text="first question", user="U_A")
    _fire(wai_env, text="second question", user="U_B")

    assert wai_env["say"].call_count == 2
    assert wai_env["say"].call_args_list[0].kwargs["text"] == ANSWER
    assert wai_env["say"].call_args_list[1].kwargs["text"] == ANSWER

    rows = sorted(_genaitexts(db), key=lambda r: r.id)
    assert [r.prompt for r in rows] == ["first question", "second question"]
    assert [r.slack_user_slack_id for r in rows] == ["U_A", "U_B"]
    # the second invocation's context saw the first one's audit row, and its own
    first_summary, second_summary = _summary_prompts(wai_env["prompts"])
    assert "first question" in first_summary
    assert "first question" in second_summary
    assert "second question" in second_summary


def test_wai_says_the_bad_response_notice_and_stamps_the_row_on_a_null_answer(
    wai_env, db, monkeypatch
):
    monkeypatch.setattr(
        commands, "execute_workflow", lambda *a, **k: None
    )

    _fire(wai_env, text="unanswerable")

    wai_env["say"].assert_called_once()
    assert "feel bad for trying" in wai_env["say"].call_args.args[0]
    rows = _genaitexts(db)
    assert len(rows) == 1
    assert rows[0].response == "WARNING: bad response produced"


# ---------------------------------------------------------------- 00e4b7f8
#
# FR 00e4b7f8, load-bearing under LB2, requires that `/wai` store NO
# per-conversation transcript, turn history, or agent definition, and that
# `genaitext` be "written as an audit log and NEVER read back as conversation
# memory".
#
# It does not hold on main@5473180b. get_slack_channel_context() reads the
# channel's genaitext rows and generate_summary() folds their prompt/response
# into the next answer's prompt, so genaitext IS read back as conversation
# memory. That is what the tests below pin, as xfail(strict=True): they are the
# requirement's own assertion, and it fails. When someone changes `/wai` so the
# guarantee holds, they go xpass-strict and the suite goes red until the
# requirement and these tests are updated together.


def test_slack_channel_context_reads_back_the_channels_genaitext_rows(db):
    _seed_genaitext(db, "earlier question", "earlier answer")
    _seed_genaitext(db, "elsewhere", "other channel", channel=OTHER_CHANNEL)

    rows = asyncio.run(get_slack_channel_context(CHANNEL))

    assert [(r.prompt, r.response) for r in rows] == [
        ("earlier question", "earlier answer")
    ]


def test_wai_answer_is_influenced_by_a_previous_wai_answer(wai_env, db):
    _seed_genaitext(db, "what is the password", "hunter2")

    _fire(wai_env, text="and what is the password again?")

    summary_prompt = _summary_prompts(wai_env["prompts"])[0]
    # the prior answer is in the prompt the model is given, verbatim
    assert "hunter2" in summary_prompt
    # and the summary it produced rides along into the answer prompt
    (answer_prompt,) = _answer_prompts(wai_env["prompts"])
    assert "SUMMARY" in answer_prompt


@pytest.mark.xfail(
    strict=True,
    reason=(
        "FR 00e4b7f8 is not met on main: /wai reads its own genaitext audit "
        "log back as conversation memory (temporal/slack/activity.py "
        "get_slack_channel_context -> db/dal/genai_dal.py "
        "get_genai_texts_by_slack_channel -> temporal/ai/activity.py "
        "generate_summary). M1 is onboarding and must not change /wai, so this "
        "is recorded as a violated guarantee, not a passing behavior."
    ),
)
def test_wai_never_reads_genaitext_back_as_conversation_memory(wai_env, db):
    _seed_genaitext(db, "earlier question", "earlier answer")
    _seed_slack_message(db, "the oars are in the blue locker")

    _fire(wai_env, text="where are the oars?")

    every_prompt = "\n".join(wai_env["prompts"])
    # FR 00e4b7f8: an audit log, never read back.
    assert "earlier question" not in every_prompt
    assert "earlier answer" not in every_prompt


@pytest.mark.xfail(
    strict=True,
    reason=(
        "FR 00e4b7f8's companion gap: /wai's context comes only from genaitext "
        "rows, never from the channel's stored Slack messages (get_slack_"
        "channel_context queries GenAIText, not SlackMessage)."
    ),
)
def test_wai_answer_is_grounded_in_the_channels_stored_slack_messages(
    wai_env, db
):
    _seed_slack_message(db, "the oars are in the blue locker")

    _fire(wai_env, text="where are the oars?")

    every_prompt = "\n".join(wai_env["prompts"])
    assert "the oars are in the blue locker" in every_prompt


def test_wai_stores_no_row_beyond_its_own_audit_rows(wai_env, db):
    """The part of 00e4b7f8 that DOES hold: no second store, no new table.

    /wai writes exactly two rows per invocation -- a slackcommand row and a
    genaitext row -- and creates no transcript/turn-history/agent-definition
    table of its own. (SlackThreadSession and SlackChannelAgentLink are the
    @mention whagent-net path, not /wai.)
    """
    written: list[str] = []

    @event.listens_for(db, "before_cursor_execute")
    def _record(conn, cursor, statement, _params, _ctx, _many):
        if statement.lstrip().upper().startswith("INSERT"):
            written.append(statement.split(" INTO ")[1].split(" ")[0].strip('"(`'))

    try:
        _fire(wai_env, text="what is up")
        _fire(wai_env, text="what is really up")
    finally:
        event.remove(db, "before_cursor_execute", _record)

    assert sorted(set(written)) == ["fcm.genaitext", "fcm.slackcommand"]

    with Session(db) as s:
        assert len(list(s.exec(select(GenAIText)).all())) == 2
        assert len(list(s.exec(select(SlackCommand)).all())) == 2
        # nothing from the mention path was touched
        assert list(s.exec(select(SlackThreadSession)).all()) == []
        assert list(s.exec(select(SlackChannelAgentLink)).all()) == []
