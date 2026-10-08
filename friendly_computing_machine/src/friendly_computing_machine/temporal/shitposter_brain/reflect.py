"""Reflect brain job: daily, validated updates to the persona's active attributes.

Compute gathers unconsumed engagement and promoted suggestions (newest first,
capped), asks the reflector agent for ops, and validates each op against the
current attributes and the evidence it cites. Apply writes the accepted ops,
the reflector run row, the consumed-input markers, and the suggestion outcomes
in the runner's single transaction. Inputs past the cap carry over to the next run.
"""

import datetime
import json
import logging
import os
import re
import time
from collections.abc import Callable
from typing import Any

from sqlmodel import Session, select
from temporalio.client import Client

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_dal,
    shitposter_memory_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
    ShitposterPostEngagement,
    ShitposterReflectorRun,
    ShitposterSuggestion,
    ShitposterSuggestionCoarseReasonEnum,
    ShitposterSuggestionOutcomeKindEnum,
    ShitposterSuggestionStatusEnum,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterMemoryCauseKindEnum,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    attribute_cap,
    reflector_input_cap,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
    _member_names,
    _wait_for_reply,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.guardrails import (
    find_member_name,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
    ApplyOutcome,
    BrainJobInput,
    JobBody,
    register_job_body,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.control import (
    register_brain_schedule,
)
from friendly_computing_machine.src.friendly_computing_machine.whagent.client import (
    get_whagent_client,
)

logger = logging.getLogger(__name__)

REFLECT_SCHEDULE_EVERY = datetime.timedelta(days=1)
# Under the reflect job timeout in base.BRAIN_JOB_TIMEOUTS, so the agent deadline fires first.
AGENT_DEADLINE_SECONDS = 20 * 60
MAX_TEXT_CHARS = 280

OP_NAMES = frozenset({"add", "reinforce", "retire", "merge"})
ADD_KINDS = frozenset({"trait", "instruction"})

# Rejection reasons, recorded per op in ShitposterReflectorRun.rejections.
REASON_MALFORMED = "malformed"
REASON_NO_VALID_CAUSE = "no_valid_cause"
REASON_MISSING_ATTRIBUTE = "missing_attribute"
REASON_TARGETS_MEMBER = "targets_member"
REASON_INSTRUCTION = "instruction"
REASON_NEGATIVE_REINFORCE = "negative_reinforce"
REASON_RETIRED_READD = "retired_readd"
REASON_KEY_EXISTS = "key_exists"
REASON_CONFLICTING_OP = "conflicting_op"
REASON_ATTRIBUTE_CAP = "attribute_cap"

# Phrasing that marks an instruction rather than a trait: imperative openers and second-person or directive modals.
_INSTRUCTION_RE = re.compile(
    r"^\s*(reply|respond|post|say|use|tell|stop|start|always|never|do|don't|ignore)\b"
    r"|\b(you|your|must|should|from now on)\b",
    re.IGNORECASE,
)

# Hook for the fold task: called with (session, persona_id, retired attribute ids, run id)
# inside the apply transaction after retire and merge ops land.
FoldHook = Callable[[Session, int, list[int], int], None]
_FOLD_HOOKS: list[FoldHook] = []


def register_fold_hook(hook: FoldHook) -> None:
    _FOLD_HOOKS.append(hook)


REFLECTOR_AGENT_DEFINITION = "shitposter-reflector"


def load_reflector_agent_id() -> str:
    """The whagent agent definition the reflect job starts on; FCM_SHITPOSTER_REFLECTOR_AGENT_ID overrides it when set."""
    override = os.environ.get("FCM_SHITPOSTER_REFLECTOR_AGENT_ID", "").strip()
    return override or REFLECTOR_AGENT_DEFINITION


def _utc(dt: datetime.datetime | None) -> datetime.datetime | None:
    # sqlite returns naive datetimes; stored timestamps are UTC
    if dt is None or dt.tzinfo is not None:
        return dt
    return dt.replace(tzinfo=datetime.timezone.utc)


def _now() -> datetime.datetime:
    return datetime.datetime.now(datetime.timezone.utc)


def _normalize(text: str) -> str:
    return " ".join(text.lower().split())


def _engagement_record(row: ShitposterPostEngagement) -> dict[str, Any]:
    total = sum(int(v) for v in (row.reactions_by_emoji or {}).values())
    return {
        "ref": f"post:{row.post_id}",
        "kind": "post",
        "id": row.post_id,
        "at": _utc(row.finalized_at),
        "stats": {
            "distinct_reactors": row.distinct_reactors,
            "distinct_repliers": row.distinct_repliers,
            "reactions_by_emoji": row.reactions_by_emoji,
            "negative_reactions": row.negative_reactions,
        },
        # negative reactions outnumber the rest, so the engagement may not back a reinforce
        "negative_dominant": row.negative_reactions > total - row.negative_reactions,
    }


def _suggestion_record(row: ShitposterSuggestion) -> dict[str, Any]:
    return {
        "ref": f"suggestion:{row.id}",
        "kind": "suggestion",
        "id": row.id,
        "at": _utc(row.promoted_at or row.submitted_at),
        "text": row.text,
        "negative_dominant": False,
    }


def _gather(session: Session, persona_id: int) -> list[dict[str, Any]]:
    """Every unconsumed input for the persona, newest first."""
    engagements = session.exec(
        select(ShitposterPostEngagement).where(
            ShitposterPostEngagement.persona_id == persona_id,
            ShitposterPostEngagement.consumed_by_reflector_run_id.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    suggestions = session.exec(
        select(ShitposterSuggestion).where(
            ShitposterSuggestion.persona_id == persona_id,
            ShitposterSuggestion.status == ShitposterSuggestionStatusEnum.PROMOTED.value,
            ShitposterSuggestion.consumed_by_reflector_run_id.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    records = [_engagement_record(e) for e in engagements]
    records += [_suggestion_record(s) for s in suggestions]
    records.sort(key=lambda r: (r["at"], r["ref"]), reverse=True)
    return records


def _public_input(rec: dict[str, Any]) -> dict[str, Any]:
    out = {k: v for k, v in rec.items() if k not in ("at", "negative_dominant")}
    out["at"] = rec["at"].isoformat()
    return out


def _operator_retired(session: Session, persona_id: int) -> list[dict[str, Any]]:
    rows = session.exec(
        select(ShitposterPersonaAttribute).where(
            ShitposterPersonaAttribute.persona_id == persona_id,
            ShitposterPersonaAttribute.status == ShitposterAttributeStatusEnum.RETIRED.value,
            ShitposterPersonaAttribute.retired_by_operator.is_(True),  # type: ignore[attr-defined]
            ShitposterPersonaAttribute.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).all()
    return [
        {"key": r.attribute_key, "text": r.text, "retired_at": _utc(r.valid_from)}
        for r in rows
    ]


def _prompt(active: list[dict[str, Any]], inputs: list[dict[str, Any]]) -> str:
    return (
        "Update the persona's attributes from the evidence below. Reply with only one "
        'JSON object: {"ops": [...]}. Each op is one of:\n'
        '- {"op":"add","key":str,"text":str,"kind":"trait"|"instruction","cause_refs":[ref,...]}\n'
        '- {"op":"reinforce","key":str,"cause_refs":[ref,...]}\n'
        '- {"op":"retire","key":str,"cause_refs":[ref,...]}\n'
        '- {"op":"merge","from_key":str,"into_key":str,"cause_refs":[ref,...]}\n'
        "Attribute text is a trait of the persona's voice, never an instruction. "
        "Cite only refs from the evidence list. Do not name community members. "
        'Return {"ops": []} when nothing warrants a change.\n\n'
        f"Current attributes: {json.dumps(active)}\n"
        f"Evidence: {json.dumps(inputs)}"
    )


def _call_reflector(active: list[dict[str, Any]], inputs: list[dict[str, Any]]) -> str:
    client = get_whagent_client()
    deadline = time.monotonic() + AGENT_DEADLINE_SECONDS
    session = client.start_session(
        load_reflector_agent_id(), first_turn=_prompt(active, inputs)
    )
    return _wait_for_reply(client, session.session_id, 0, deadline)


def _parse_ops(text: str) -> list[Any]:
    """The reply's ops list; a reply without one fails the run (per-op problems are rejections)."""
    start, end = text.find("{"), text.rfind("}")
    if start < 0 or end < start:
        raise ValueError("reflector reply has no JSON object")
    try:
        obj = json.loads(text[start : end + 1])
    except json.JSONDecodeError as e:
        raise ValueError(f"reflector reply is not valid JSON: {e}") from e
    ops = obj.get("ops") if isinstance(obj, dict) else None
    if not isinstance(ops, list):
        raise ValueError("reflector reply has no ops list")
    return ops


def _op_name(raw: Any) -> str:
    name = raw.get("op") if isinstance(raw, dict) else None
    return name if isinstance(name, str) and name in OP_NAMES else "unknown"


def _str_field(raw: dict[str, Any], field: str) -> str:
    value = raw.get(field)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field} must be a non-empty string")
    return value.strip()


def _check_op(
    raw: Any, ctx: dict[str, Any], touched: set[str]
) -> tuple[dict[str, Any] | None, str | None, list[str]]:
    """Validate one op. Returns (accepted op or None, rejection reason or None, cited refs)."""
    try:
        if not isinstance(raw, dict):
            raise ValueError("op is not an object")
        name = _op_name(raw)
        if name == "unknown":
            raise ValueError("unknown op")
        causes = raw.get("cause_refs")
        if not isinstance(causes, list) or not all(isinstance(c, str) for c in causes):
            raise ValueError("cause_refs must be a list of strings")
        if name == "add":
            key, text = _str_field(raw, "key"), _str_field(raw, "text")
            if len(text) > MAX_TEXT_CHARS:
                raise ValueError("text too long")
            kind = raw.get("kind")
            if kind not in ADD_KINDS:
                raise ValueError("kind must be trait or instruction")
            keys = [key]
        elif name == "merge":
            keys = [_str_field(raw, "from_key"), _str_field(raw, "into_key")]
            if keys[0] == keys[1]:
                raise ValueError("cannot merge an attribute into itself")
        else:
            keys = [_str_field(raw, "key")]
    except ValueError:
        return None, REASON_MALFORMED, []

    inputs: dict[str, dict[str, Any]] = ctx["inputs"]
    causes_valid = [c for c in causes if c in inputs]
    if not causes_valid:
        return None, REASON_NO_VALID_CAUSE, []

    active: dict[str, ShitposterPersonaAttribute] = ctx["active"]
    if name != "add" and any(k not in active for k in keys):
        return None, REASON_MISSING_ATTRIBUTE, causes_valid

    if name == "add":
        if key in active:
            return None, REASON_KEY_EXISTS, causes_valid
        if find_member_name(text, ctx["member_names"]):
            return None, REASON_TARGETS_MEMBER, causes_valid
        if kind == "instruction" or _INSTRUCTION_RE.search(text):
            return None, REASON_INSTRUCTION, causes_valid
        evidence_at = max(inputs[c]["at"] for c in causes_valid)
        matches = [
            r
            for r in ctx["retired"]
            if r["key"] == key or _normalize(r["text"]) == _normalize(text)
        ]
        if matches:
            latest_retirement = max(r["retired_at"] for r in matches)
            if evidence_at < latest_retirement:
                return None, REASON_RETIRED_READD, causes_valid

    if name == "reinforce":
        if any(inputs[c]["negative_dominant"] for c in causes_valid):
            return None, REASON_NEGATIVE_REINFORCE, causes_valid

    if any(k in touched for k in keys):
        return None, REASON_CONFLICTING_OP, causes_valid
    touched.update(keys)

    if name == "add":
        accepted = {"op": "add", "key": key, "text": text, "causes": causes_valid}
    elif name == "merge":
        accepted = {
            "op": "merge",
            "from_key": keys[0],
            "into_key": keys[1],
            "from_id": active[keys[0]].id,
            "into_id": active[keys[1]].id,
            "causes": causes_valid,
        }
    else:
        accepted = {
            "op": name,
            "key": keys[0],
            "attribute_id": active[keys[0]].id,
            "causes": causes_valid,
        }
    return accepted, None, causes_valid


def _enforce_cap(
    accepted: list[dict[str, Any]],
    rejections: list[dict[str, Any]],
    active_count: int,
    cap: int,
) -> list[dict[str, Any]]:
    """Drop trailing additions until the active count fits the cap; retires and merges always stay."""
    net = active_count + sum(1 for o in accepted if o["op"] == "add") - sum(
        1 for o in accepted if o["op"] in ("retire", "merge")
    )
    kept = list(accepted)
    for i in range(len(kept) - 1, -1, -1):
        if net <= cap:
            break
        if kept[i]["op"] != "add":
            continue
        dropped = kept.pop(i)
        net -= 1
        rejections.append(
            {"op": "add", "reason": REASON_ATTRIBUTE_CAP, "causes": dropped["causes"]}
        )
    if net > cap:
        logger.warning("reflect: %d active attributes still exceed cap %d", net, cap)
    return kept


def _validate(
    raw_ops: list[Any],
    inputs: dict[str, dict[str, Any]],
    active: list[ShitposterPersonaAttribute],
    retired: list[dict[str, Any]],
    member_names: list[str],
    cap: int,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    ctx = {
        "inputs": inputs,
        "active": {a.attribute_key: a for a in active},
        "retired": retired,
        "member_names": member_names,
    }
    touched: set[str] = set()
    accepted: list[dict[str, Any]] = []
    rejections: list[dict[str, Any]] = []
    for raw in raw_ops:
        op, reason, causes = _check_op(raw, ctx, touched)
        if op is None:
            rejections.append({"op": _op_name(raw), "reason": reason, "causes": causes})
        else:
            accepted.append(op)
    accepted = _enforce_cap(accepted, rejections, len(active), cap)
    return accepted, rejections


def _compute(params: BrainJobInput) -> dict[str, Any]:
    persona_id = params.persona_id
    with SessionManager() as session:
        records = _gather(session, persona_id)
        taken = records[: reflector_input_cap()]
        carried = len(records) - len(taken)
        if not taken:
            logger.info("reflect: no new input for persona=%s; agent not called", persona_id)
            return {
                "no_input": True,
                "persona_id": persona_id,
                "input_count": 0,
                "carried_over_count": 0,
                "inputs": [],
                "ops": [],
                "rejections": [],
            }
        active = shitposter_memory_dal.active_attributes(persona_id, session=session)
        active_view = [{"key": a.attribute_key, "text": a.text} for a in active]
        retired = _operator_retired(session, persona_id)
        member_names = _member_names()

    # the session is closed before the agent call so no transaction spans it
    raw_ops = _parse_ops(
        _call_reflector(active_view, [_public_input(r) for r in taken])
    )
    accepted, rejections = _validate(
        raw_ops,
        {r["ref"]: r for r in taken},
        active,
        retired,
        member_names,
        attribute_cap(),
    )
    logger.info(
        "reflect computed persona=%s inputs=%d carried=%d accepted=%d rejected=%d",
        persona_id,
        len(taken),
        carried,
        len(accepted),
        len(rejections),
    )
    return {
        "no_input": False,
        "persona_id": persona_id,
        "input_count": len(taken),
        "carried_over_count": carried,
        "inputs": [
            {"ref": r["ref"], "kind": r["kind"], "id": r["id"]} for r in taken
        ],
        "ops": accepted,
        "rejections": rejections,
    }


def _cause_args(causes: list[str]) -> dict[str, Any]:
    kind, _, ident = causes[0].partition(":")
    cause_ref = ",".join(causes)
    if kind == "post":
        return {
            "cause_kind": ShitposterMemoryCauseKindEnum.POST_ENGAGEMENT.value,
            "cause_post_id": int(ident),
            "cause_ref": cause_ref,
        }
    return {
        "cause_kind": ShitposterMemoryCauseKindEnum.SUGGESTION.value,
        "cause_post_id": None,
        "cause_ref": cause_ref,
    }


def _suggestion_coarse_reason(
    suggestion_ref: str, rejections: list[dict[str, Any]]
) -> str:
    """Coarse reason for a declined suggestion, from the first rejection that cites it."""
    reasons = [r["reason"] for r in rejections if suggestion_ref in r.get("causes", [])]
    if REASON_TARGETS_MEMBER in reasons:
        return ShitposterSuggestionCoarseReasonEnum.UNSAFE.value
    if reasons and reasons[0] in (REASON_KEY_EXISTS, REASON_CONFLICTING_OP):
        return ShitposterSuggestionCoarseReasonEnum.DUPLICATE.value
    if reasons and reasons[0] in (REASON_INSTRUCTION, REASON_RETIRED_READD, REASON_NEGATIVE_REINFORCE):
        return ShitposterSuggestionCoarseReasonEnum.OFF_TOPIC.value
    return ShitposterSuggestionCoarseReasonEnum.OTHER.value


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    persona_id = payload["persona_id"]
    now = _now()
    if payload.get("no_input"):
        session.add(
            ShitposterReflectorRun(
                brain_job_run_id=run_id,
                persona_id=persona_id,
                input_count=0,
                carried_over_count=0,
                applied_count=0,
                rejection_count=0,
                rejections=[],
            )
        )
        session.flush()
        return ApplyOutcome(
            status=ShitposterBrainJobStatus.NO_OP.value,
            details={"input_count": 0, "applied": 0, "rejected": 0},
        )

    reflector_run_id = str(run_id)
    ops: list[dict[str, Any]] = payload["ops"]
    rejections: list[dict[str, Any]] = payload["rejections"]
    retired_ids: list[int] = []

    # retires and merges first so a freed key can be re-added within one run's op set
    for op in ops:
        if op["op"] == "retire":
            shitposter_memory_dal._retire_attribute(
                session,
                op["attribute_id"],
                reflector_run_id=reflector_run_id,
                retired_by_operator=False,
                now=now,
                **_cause_args(op["causes"]),
            )
            retired_ids.append(op["attribute_id"])
    for op in ops:
        if op["op"] == "merge":
            shitposter_memory_dal._merge_attributes(
                session,
                op["from_id"],
                op["into_id"],
                reflector_run_id=reflector_run_id,
                now=now,
                **_cause_args(op["causes"]),
            )
            retired_ids.append(op["from_id"])
    for op in ops:
        if op["op"] == "reinforce":
            shitposter_memory_dal._reinforce_attribute(
                session,
                op["attribute_id"],
                reflector_run_id=reflector_run_id,
                now=now,
                **_cause_args(op["causes"]),
            )
    for op in ops:
        if op["op"] == "add":
            shitposter_memory_dal._add_attribute(
                session,
                persona_id=persona_id,
                attribute_key=op["key"],
                text=op["text"],
                reflector_run_id=reflector_run_id,
                now=now,
                **_cause_args(op["causes"]),
            )

    for hook in _FOLD_HOOKS:
        hook(session, persona_id, retired_ids, run_id)

    session.add(
        ShitposterReflectorRun(
            brain_job_run_id=run_id,
            persona_id=persona_id,
            input_count=payload["input_count"],
            carried_over_count=payload["carried_over_count"],
            applied_count=len(ops),
            rejection_count=len(rejections),
            rejections=[{"op": r["op"], "reason": r["reason"]} for r in rejections],
        )
    )

    # consumed inputs are the ones this run read; carried-over inputs stay unconsumed
    engagement_ids = [i["id"] for i in payload["inputs"] if i["kind"] == "post"]
    suggestion_ids = {i["id"]: i["ref"] for i in payload["inputs"] if i["kind"] == "suggestion"}
    if engagement_ids:
        for row in session.exec(
            select(ShitposterPostEngagement).where(
                ShitposterPostEngagement.post_id.in_(engagement_ids)  # type: ignore[attr-defined]
            )
        ).all():
            row.consumed_by_reflector_run_id = run_id
            session.add(row)
    if suggestion_ids:
        for row in session.exec(
            select(ShitposterSuggestion).where(
                ShitposterSuggestion.id.in_(list(suggestion_ids))  # type: ignore[attr-defined]
            )
        ).all():
            row.consumed_by_reflector_run_id = reflector_run_id
            session.add(row)
        session.flush()
        applied_refs = {c for op in ops for c in op["causes"]}
        for sid, ref in suggestion_ids.items():
            if ref in applied_refs:
                shitposter_dal.stage_suggestion_outcome(
                    session,
                    sid,
                    ShitposterSuggestionOutcomeKindEnum.APPLIED.value,
                    None,
                    now,
                )
            else:
                shitposter_dal.stage_suggestion_outcome(
                    session,
                    sid,
                    ShitposterSuggestionOutcomeKindEnum.DECLINED.value,
                    _suggestion_coarse_reason(ref, rejections),
                    now,
                )
    session.flush()

    details = {
        "input_count": payload["input_count"],
        "carried_over_count": payload["carried_over_count"],
        "applied": len(ops),
        "rejected": len(rejections),
    }
    logger.info("reflect applied run=%s %s", run_id, details)
    return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details=details)


async def register_reflect_schedule(
    client: Client,
    task_queue: str,
    persona_id: int,
    app_env: str | None = None,
) -> None:
    """Create the daily reflect schedule for one persona; no-op if it exists."""
    await register_brain_schedule(
        client,
        task_queue,
        persona_id,
        ShitposterBrainJobKind.REFLECT.value,
        every=REFLECT_SCHEDULE_EVERY,
        app_env=app_env,
    )


register_job_body(
    ShitposterBrainJobKind.REFLECT,
    JobBody(compute=_compute, apply=_apply),
)
