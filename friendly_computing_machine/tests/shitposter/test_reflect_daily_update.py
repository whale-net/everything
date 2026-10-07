"""Reflect brain job: validated daily attribute updates within the attribute cap.

Validation cases run `_validate` directly on constructed attribute rows. The
end-to-end cases run compute then apply against an in-memory SQLite DB with the
reflector agent call replaced by a canned reply.
"""

import datetime
import json

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobRun,
    ShitposterChannelOptIn,
    ShitposterPersona,
    ShitposterPersonaRevision,
    ShitposterPost,
    ShitposterPostEngagement,
    ShitposterReflectorRun,
    ShitposterSuggestion,
    ShitposterSuggestionReplyOutbox,
    ShitposterSuggestionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterMemoryChange,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackUser,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    reflect,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    BrainJobInput,
)

TABLES = [
    SlackUser.__table__,
    SlackChannel.__table__,
    ShitposterChannelOptIn.__table__,
    ShitposterPersona.__table__,
    ShitposterPersonaRevision.__table__,
    ShitposterPost.__table__,
    ShitposterPostEngagement.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterReflectorRun.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterMemoryChange.__table__,
    ShitposterSuggestion.__table__,
    ShitposterSuggestionReplyOutbox.__table__,
]

NOW = datetime.datetime.now(datetime.timezone.utc)


def _ago(hours: float) -> datetime.datetime:
    return NOW - datetime.timedelta(hours=hours)


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
    monkeypatch.setenv("FCM_SHITPOSTER_REFLECTOR_INPUT_CAP", "200")
    monkeypatch.setenv("FCM_SHITPOSTER_ATTRIBUTE_CAP", "20")
    return engine


def _attr(id_: int, key: str, text: str) -> ShitposterPersonaAttribute:
    return ShitposterPersonaAttribute(
        id=id_,
        persona_id=1,
        attribute_key=key,
        text=text,
        status=ShitposterAttributeStatusEnum.ACTIVE.value,
    )


def _input(ref: str, at: datetime.datetime, negative: bool = False) -> dict:
    return {
        "ref": ref,
        "kind": ref.split(":")[0],
        "id": int(ref.split(":")[1]),
        "at": at,
        "negative_dominant": negative,
        "text": "",
    }


INPUTS = {
    "post:1": _input("post:1", _ago(2)),
    "post:2": _input("post:2", _ago(3), negative=True),
    "suggestion:5": _input("suggestion:5", _ago(1)),
}
ACTIVE = [_attr(1, "humor", "dry humor"), _attr(2, "pace", "short sentences")]


def _validate_one(raw, retired=None, cap=20, active=ACTIVE):
    return reflect._validate(
        [raw],
        INPUTS,
        active,
        retired or [],
        ["Alice"],
        cap,
    )


@pytest.mark.parametrize(
    "raw, reason",
    [
        ("not an object", "malformed"),
        ({"op": "sing", "cause_refs": ["post:1"]}, "malformed"),
        ({"op": "add", "key": "x", "text": "", "kind": "trait", "cause_refs": ["post:1"]}, "malformed"),
        ({"op": "add", "key": "x", "text": "fun", "kind": "maybe", "cause_refs": ["post:1"]}, "malformed"),
        ({"op": "add", "key": "x", "text": "fun", "kind": "trait", "cause_refs": "post:1"}, "malformed"),
        ({"op": "merge", "from_key": "humor", "into_key": "humor", "cause_refs": ["post:1"]}, "malformed"),
        ({"op": "reinforce", "key": "nope", "cause_refs": ["post:1"]}, "missing_attribute"),
        ({"op": "reinforce", "key": "humor", "cause_refs": ["post:99"]}, "no_valid_cause"),
        ({"op": "add", "key": "fan", "text": "Alice loves this", "kind": "trait", "cause_refs": ["post:1"]}, "targets_member"),
        ({"op": "add", "key": "rule", "text": "You must reply in all caps", "kind": "trait", "cause_refs": ["post:1"]}, "instruction"),
        ({"op": "add", "key": "x", "text": "dry wit", "kind": "instruction", "cause_refs": ["post:1"]}, "instruction"),
        ({"op": "reinforce", "key": "humor", "cause_refs": ["post:2"]}, "negative_reinforce"),
        ({"op": "add", "key": "humor", "text": "more humor", "kind": "trait", "cause_refs": ["post:1"]}, "key_exists"),
    ],
)
def test_rejection_reasons(raw, reason):
    accepted, rejections = _validate_one(raw)
    assert accepted == []
    assert [r["reason"] for r in rejections] == [reason]


def test_valid_reinforce_is_accepted():
    accepted, rejections = _validate_one(
        {"op": "reinforce", "key": "humor", "cause_refs": ["post:1"]}
    )
    assert rejections == []
    assert accepted[0]["op"] == "reinforce" and accepted[0]["attribute_id"] == 1


def test_readd_of_operator_retired_item_rejected_when_evidence_predates_retirement():
    retired = [{"key": "mood", "text": "Gloomy tone", "retired_at": _ago(1)}]
    raw = {"op": "add", "key": "mood", "text": "gloomy tone", "kind": "trait", "cause_refs": ["post:1"]}
    _, rejections = _validate_one(raw, retired=retired)
    assert [r["reason"] for r in rejections] == ["retired_readd"]


def test_readd_allowed_when_evidence_postdates_retirement():
    retired = [{"key": "mood", "text": "Gloomy tone", "retired_at": _ago(10)}]
    raw = {"op": "add", "key": "mood", "text": "Gloomy tone", "kind": "trait", "cause_refs": ["post:1"]}
    accepted, rejections = _validate_one(raw, retired=retired)
    assert rejections == []
    assert accepted[0]["key"] == "mood"


def test_conflicting_ops_on_one_key_keep_only_the_first():
    accepted, rejections = reflect._validate(
        [
            {"op": "reinforce", "key": "humor", "cause_refs": ["post:1"]},
            {"op": "retire", "key": "humor", "cause_refs": ["post:1"]},
        ],
        INPUTS,
        ACTIVE,
        [],
        [],
        20,
    )
    assert [o["op"] for o in accepted] == ["reinforce"]
    assert [r["reason"] for r in rejections] == ["conflicting_op"]


def test_cap_rejects_excess_additions_when_nothing_retires():
    raw = {"op": "add", "key": "mood", "text": "Gloomy tone", "kind": "trait", "cause_refs": ["post:1"]}
    accepted, rejections = _validate_one(raw, cap=2)
    assert accepted == []
    assert [r["reason"] for r in rejections] == ["attribute_cap"]


def test_cap_admits_addition_paired_with_retirement():
    ops = [
        {"op": "retire", "key": "pace", "cause_refs": ["post:1"]},
        {"op": "add", "key": "mood", "text": "Gloomy tone", "kind": "trait", "cause_refs": ["post:1"]},
    ]
    accepted, rejections = reflect._validate(ops, INPUTS, ACTIVE, [], [], 2)
    assert rejections == []
    assert [o["op"] for o in accepted] == ["retire", "add"]


def test_cap_drops_only_the_excess_additions_from_the_end():
    ops = [
        {"op": "add", "key": "a1", "text": "Odd cadence", "kind": "trait", "cause_refs": ["post:1"]},
        {"op": "add", "key": "a2", "text": "Gloomy tone", "kind": "trait", "cause_refs": ["post:1"]},
    ]
    accepted, rejections = reflect._validate(ops, INPUTS, ACTIVE, [], [], 3)
    assert [o["key"] for o in accepted] == ["a1"]
    assert [r["reason"] for r in rejections] == ["attribute_cap"]


def test_parse_ops_requires_an_ops_list():
    assert reflect._parse_ops('noise {"ops": []} trailing') == []
    with pytest.raises(ValueError):
        reflect._parse_ops('{"notops": 1}')
    with pytest.raises(ValueError):
        reflect._parse_ops("no json here")


# ---- end to end against the DB --------------------------------------------


def _seed(engine, attributes=True) -> dict:
    with Session(engine) as s:
        persona = ShitposterPersona(name="shitposter")
        s.add(persona)
        s.flush()
        revision = ShitposterPersonaRevision(persona_id=persona.id, persona_text="seed")
        s.add(revision)
        s.flush()
        s.add(SlackChannel(id=1, slack_id="C1", name="general", channel_type="channel"))
        s.flush()
        ids = {"persona_id": persona.id, "revision_id": revision.id}
        if attributes:
            s.commit()
            shitposter_memory_dal.add_attribute(
                persona.id, "humor", "dry humor", "operator", cause_ref="seed", session=s
            )
            shitposter_memory_dal.add_attribute(
                persona.id, "pace", "short sentences", "operator", cause_ref="seed", session=s
            )
        s.commit()
        post_ids = []
        for n, (finalized, neg, emoji) in enumerate(
            [(_ago(2), 0, {"laugh": 3}), (_ago(3), 2, {"thumbsdown": 2, "laugh": 1})], start=1
        ):
            post = ShitposterPost(
                slack_channel_id=1,
                slack_message_ts=f"100.{n}",
                persona_id=persona.id,
                persona_revision_id=revision.id,
                trigger="scheduled",
                principal_iss="iss",
                principal_sub="sub",
                principal_kind="service",
                whagent_session_id="sess",
                created_at=_ago(30),
            )
            s.add(post)
            s.flush()
            s.add(
                ShitposterPostEngagement(
                    post_id=post.id,
                    persona_id=persona.id,
                    finalized_at=finalized,
                    distinct_reactors=sum(emoji.values()),
                    reactions_by_emoji=emoji,
                    distinct_repliers=0,
                    negative_reactions=neg,
                )
            )
            post_ids.append(post.id)
        s.add(
            ShitposterSuggestion(
                persona_id=persona.id,
                slack_channel_id=1,
                slack_message_ts="200.1",
                submitter_slack_user_id="U1",
                text="more cat puns",
                status=ShitposterSuggestionStatusEnum.PROMOTED.value,
                submitted_at=_ago(48),
                expires_at=NOW + datetime.timedelta(days=5),
                status_changed_at=_ago(1),
                promoted_at=_ago(1),
            )
        )
        s.commit()
        suggestion = s.exec(select(ShitposterSuggestion)).one()
        ids.update(post_ids=post_ids, suggestion_id=suggestion.id)
        return ids


def _run(session: Session, persona_id: int, status: str = "running") -> int:
    run = ShitposterBrainJobRun(
        persona_id=persona_id,
        job_kind="reflect",
        trigger="operator",
        status=status,
        started_at=NOW,
        finished_at=None if status == "running" else NOW,
    )
    session.add(run)
    session.commit()
    return run.id


def _canned_reply(monkeypatch, ops, calls=None):
    def fake(active, inputs):
        if calls is not None:
            calls.append(inputs)
        return json.dumps({"ops": ops})

    monkeypatch.setattr(reflect, "_call_reflector", fake)


def test_no_input_records_no_op_and_never_calls_the_agent(engine, monkeypatch):
    ids = _seed(engine, attributes=True)
    # consume every seeded input so the next compute finds nothing new
    with Session(engine) as s:
        earlier_run = _run(s, ids["persona_id"], status="succeeded")
        for row in s.exec(select(ShitposterPostEngagement)).all():
            row.consumed_by_reflector_run_id = earlier_run
            s.add(row)
        sug = s.exec(select(ShitposterSuggestion)).one()
        sug.consumed_by_reflector_run_id = str(earlier_run)
        s.add(sug)
        s.commit()

    def boom(active, inputs):
        raise AssertionError("agent must not be called without new input")

    monkeypatch.setattr(reflect, "_call_reflector", boom)
    payload = reflect._compute(BrainJobInput(persona_id=ids["persona_id"], job_kind="reflect"))
    assert payload["no_input"] is True
    with Session(engine) as s:
        run_id = _run(s, ids["persona_id"])
        outcome = reflect._apply(s, run_id, payload)
        s.commit()
        assert outcome.status == "no_op"
        row = s.exec(select(ShitposterReflectorRun)).one()
        assert (row.input_count, row.applied_count, row.rejection_count) == (0, 0, 0)


def test_run_applies_ops_consumes_inputs_and_settles_suggestion(engine, monkeypatch):
    ids = _seed(engine)
    p1, p2 = ids["post_ids"]
    sref = f"suggestion:{ids['suggestion_id']}"
    _canned_reply(
        monkeypatch,
        [
            {"op": "reinforce", "key": "humor", "cause_refs": [f"post:{p1}"]},
            {"op": "add", "key": "puns", "text": "Fond of wordplay", "kind": "trait", "cause_refs": [sref]},
        ],
    )
    with Session(engine) as s:
        run_id = _run(s, ids["persona_id"])
    payload = reflect._compute(BrainJobInput(persona_id=ids["persona_id"], job_kind="reflect"))
    assert payload["input_count"] == 3 and payload["carried_over_count"] == 0

    with Session(engine) as s:
        outcome = reflect._apply(s, run_id, payload)
        s.commit()
        assert outcome.status == "succeeded"

        keys = {a.attribute_key for a in shitposter_memory_dal.active_attributes(ids["persona_id"], session=s)}
        assert keys == {"humor", "pace", "puns"}
        changes = s.exec(select(ShitposterMemoryChange).where(ShitposterMemoryChange.reflector_run_id == str(run_id))).all()
        assert sorted(c.operation for c in changes) == ["add", "reinforce"]
        assert all(c.cause_ref for c in changes)

        for row in s.exec(select(ShitposterPostEngagement)).all():
            assert row.consumed_by_reflector_run_id == run_id
        sug = s.exec(select(ShitposterSuggestion)).one()
        assert sug.consumed_by_reflector_run_id == str(run_id)
        assert sug.status == ShitposterSuggestionStatusEnum.APPLIED.value
        outbox = s.exec(select(ShitposterSuggestionReplyOutbox)).one()
        assert outbox.kind == "applied"
        rr = s.exec(select(ShitposterReflectorRun)).one()
        assert (rr.input_count, rr.applied_count, rr.rejection_count) == (3, 2, 0)


def test_excess_inputs_carry_over_to_the_next_run(engine, monkeypatch):
    ids = _seed(engine)
    monkeypatch.setenv("FCM_SHITPOSTER_REFLECTOR_INPUT_CAP", "1")
    _canned_reply(monkeypatch, [])
    with Session(engine) as s:
        run_id = _run(s, ids["persona_id"])
    payload = reflect._compute(BrainJobInput(persona_id=ids["persona_id"], job_kind="reflect"))
    assert (payload["input_count"], payload["carried_over_count"]) == (1, 2)
    # newest first: the promoted suggestion is the one read this run
    assert payload["inputs"][0]["kind"] == "suggestion"

    with Session(engine) as s:
        reflect._apply(s, run_id, payload)
        s.commit()
        for row in s.exec(select(ShitposterPostEngagement)).all():
            assert row.consumed_by_reflector_run_id is None
        sug = s.exec(select(ShitposterSuggestion)).one()
        assert sug.consumed_by_reflector_run_id == str(run_id)
        # no op cited the suggestion, so it is declined rather than applied
        assert sug.status == ShitposterSuggestionStatusEnum.DECLINED.value
        assert s.exec(select(ShitposterSuggestionReplyOutbox)).one().coarse_reason == "other"


def test_failed_apply_leaves_attributes_and_inputs_unchanged(engine, monkeypatch):
    ids = _seed(engine)
    p1, _ = ids["post_ids"]
    # the valid retire writes first; the reinforce of a missing row then fails the whole apply
    _canned_reply(monkeypatch, [])
    with Session(engine) as s:
        run_id = _run(s, ids["persona_id"])
        pace = s.exec(
            select(ShitposterPersonaAttribute).where(
                ShitposterPersonaAttribute.attribute_key == "pace",
                ShitposterPersonaAttribute.valid_to.is_(None),  # type: ignore[union-attr]
            )
        ).one()
    payload = {
        "no_input": False,
        "persona_id": ids["persona_id"],
        "input_count": 3,
        "carried_over_count": 0,
        "inputs": [
            {"ref": f"post:{p1}", "kind": "post", "id": p1},
            {"ref": f"post:{ids['post_ids'][1]}", "kind": "post", "id": ids["post_ids"][1]},
            {"ref": f"suggestion:{ids['suggestion_id']}", "kind": "suggestion", "id": ids["suggestion_id"]},
        ],
        "ops": [
            {"op": "retire", "key": "pace", "attribute_id": pace.id, "causes": [f"post:{p1}"]},
            {"op": "reinforce", "key": "ghost", "attribute_id": 999, "causes": [f"post:{p1}"]},
        ],
        "rejections": [],
    }
    with Session(engine) as s:
        with pytest.raises(LookupError):
            reflect._apply(s, run_id, payload)
        s.rollback()

    with Session(engine) as s:
        keys = {a.attribute_key for a in shitposter_memory_dal.active_attributes(ids["persona_id"], session=s)}
        assert keys == {"humor", "pace"}
        assert s.exec(select(ShitposterReflectorRun)).all() == []
        assert all(
            r.consumed_by_reflector_run_id is None
            for r in s.exec(select(ShitposterPostEngagement)).all()
        )
        assert s.exec(select(ShitposterSuggestion)).one().status == ShitposterSuggestionStatusEnum.PROMOTED.value
