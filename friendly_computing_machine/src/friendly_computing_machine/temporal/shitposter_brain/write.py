"""Write brain job: drafts a batch of posts from the latest context snapshot into the draft queue.

Compute reads the latest snapshot and calls the writer agent (no DB writes).
Each reply item is validated; a malformed item or a repeated rank is dropped and
counted in the run's details. A run with no valid draft ends as failed.
Scheduled posts pick from this queue (see temporal/shitposter/activity.py).
"""

import datetime
import json
import logging
import os
import time
from typing import Any

from sqlmodel import Session
from temporalio.client import Client

from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_snapshot_dal,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobStatus,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_draft import (
    ShitposterDraft,
)
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    draft_batch_size,
    write_cadence_hours,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.activity import (
    _wait_for_reply,
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

AGENT_DEADLINE_SECONDS = 20 * 60
MAX_POST_CHARS = 280
WRITER_AGENT_DEFINITION = "shitposter-drafter"


def load_writer_agent_id() -> str:
    """The whagent agent definition the write job starts on; FCM_SHITPOSTER_WRITER_AGENT_ID overrides it when set."""
    override = os.environ.get("FCM_SHITPOSTER_WRITER_AGENT_ID", "").strip()
    return override or WRITER_AGENT_DEFINITION


def _instruction(batch: int) -> str:
    return (
        f"Write a batch of {batch} standalone posts in the persona's voice, ranked best first. "
        "Reply with only the JSON array."
    )


def _call_writer(snapshot_text: str, batch: int) -> tuple[str, str]:
    """Start a writer session with the snapshot as its first turn; return (session_id, reply)."""
    client = get_whagent_client()
    deadline = time.monotonic() + AGENT_DEADLINE_SECONDS
    instruction = _instruction(batch)
    pinned = None
    if client.supports_pinned_context():
        pinned, first_turn = snapshot_text, instruction
    else:
        first_turn = f"{snapshot_text}\n\n{instruction}" if snapshot_text else instruction
    session = client.start_session(
        load_writer_agent_id(), first_turn=first_turn, pinned_context=pinned
    )
    reply = _wait_for_reply(client, session.session_id, 0, deadline)
    return session.session_id, reply


def _parse_array(text: str) -> list[Any] | None:
    """The reply's JSON array, or None when the reply carries no parseable array."""
    start, end = text.find("["), text.rfind("]")
    if start < 0 or end < start:
        return None
    try:
        obj = json.loads(text[start : end + 1])
    except json.JSONDecodeError:
        return None
    return obj if isinstance(obj, list) else None


def validate_drafts(items: list[Any], batch: int) -> tuple[list[dict[str, Any]], int]:
    """Valid drafts best rank first, at most `batch` of them, and the count of dropped items."""
    seen_ranks: set[int] = set()
    valid: list[dict[str, Any]] = []
    dropped = 0
    for raw in items:
        draft = _check_item(raw)
        if draft is None or draft["rank"] in seen_ranks:
            dropped += 1
            continue
        seen_ranks.add(draft["rank"])
        valid.append(draft)
    valid.sort(key=lambda d: d["rank"])
    dropped += max(0, len(valid) - batch)
    return valid[:batch], dropped


def _check_item(raw: Any) -> dict[str, Any] | None:
    if not isinstance(raw, dict):
        return None
    text, rank = raw.get("text"), raw.get("rank")
    if not isinstance(text, str) or not text.strip() or len(text.strip()) > MAX_POST_CHARS:
        return None
    # bool is an int subclass; a true/false rank is malformed
    if not isinstance(rank, int) or isinstance(rank, bool) or rank < 1:
        return None
    return {"text": text.strip(), "rank": rank}


def _compute(params: BrainJobInput) -> dict[str, Any]:
    with SessionManager() as session:
        snapshot = shitposter_snapshot_dal.latest_snapshot(params.persona_id, session=session)
        if snapshot is None:
            raise RuntimeError(f"persona {params.persona_id} has no context snapshot to write from")
        snapshot_id, snapshot_text = snapshot.id, snapshot.rendered_text
    batch = draft_batch_size()
    session_id, reply = _call_writer(snapshot_text, batch)
    items = _parse_array(reply)
    if items is None:
        return {
            "persona_id": params.persona_id,
            "snapshot_id": snapshot_id,
            "whagent_session_id": session_id,
            "drafts": [],
            "dropped": 0,
            "error": "writer reply has no JSON array",
        }
    drafts, dropped = validate_drafts(items, batch)
    logger.info(
        "write computed persona=%s snapshot=%s valid=%d dropped=%d",
        params.persona_id,
        snapshot_id,
        len(drafts),
        dropped,
    )
    return {
        "persona_id": params.persona_id,
        "snapshot_id": snapshot_id,
        "whagent_session_id": session_id,
        "drafts": drafts,
        "dropped": dropped,
        "error": None,
    }


def _apply(session: Session, run_id: int, payload: dict[str, Any]) -> ApplyOutcome:
    persona_id = int(payload["persona_id"])
    snapshot_id = int(payload["snapshot_id"])
    drafts = payload["drafts"]
    details = {
        "snapshot_id": snapshot_id,
        "drafted": len(drafts),
        "dropped": payload["dropped"],
        "whagent_session_id": payload["whagent_session_id"],
    }
    if not drafts:
        return ApplyOutcome(
            status=ShitposterBrainJobStatus.FAILED.value,
            details=details,
            error=payload.get("error") or "writer batch had no valid draft",
        )
    now = datetime.datetime.now(datetime.timezone.utc)
    for draft in drafts:
        session.add(
            ShitposterDraft(
                persona_id=persona_id,
                snapshot_id=snapshot_id,
                brain_job_run_id=run_id,
                text=draft["text"],
                rank=draft["rank"],
                created_at=now,
            )
        )
    return ApplyOutcome(status=ShitposterBrainJobStatus.SUCCEEDED.value, details=details)


async def register_write_schedule(
    client: Client,
    task_queue: str,
    persona_id: int,
    app_env: str | None = None,
) -> None:
    """Create the writer schedule for one persona (every FCM_SHITPOSTER_WRITE_CADENCE_HOURS); no-op if it exists."""
    await register_brain_schedule(
        client,
        task_queue,
        persona_id,
        ShitposterBrainJobKind.WRITE.value,
        every=datetime.timedelta(hours=write_cadence_hours()),
        app_env=app_env,
    )


register_job_body(
    ShitposterBrainJobKind.WRITE,
    JobBody(compute=_compute, apply=_apply),
)
