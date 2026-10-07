"""Activities for the Shitposter generation pipeline (see workflow.py)."""

from dataclasses import dataclass
from typing import Optional

from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.types import (
    ShitpostParams,
    ShitpostResult,
)


@dataclass
class GateActivityResult:
    allowed: bool
    reason: Optional[str] = None


@dataclass
class PersonaResult:
    persona_id: int
    persona_revision_id: int
    persona_text: str


@dataclass
class GenerateParams:
    params: ShitpostParams
    persona_text: str
    deadline_seconds: float
    # tells the model to try again after a guardrail block
    attempt: int = 0


@dataclass
class GenerateResult:
    ok: bool
    text: str = ""
    whagent_session_id: Optional[str] = None
    error: Optional[str] = None
    timed_out: bool = False


@dataclass
class GuardrailResult:
    allowed: bool
    reason: Optional[str] = None


@dataclass
class PostParams:
    params: ShitpostParams
    text: str
    persona: PersonaResult
    whagent_session_id: str


@dataclass
class NoticeParams:
    channel_slack_id: str
    slack_user_id: str
    text: str
    thread_ts: Optional[str] = None


@activity.defn
async def shitposter_gate_activity(channel_slack_id: str) -> GateActivityResult:
    """Uncached gate read."""
    raise NotImplementedError


@activity.defn
async def resolve_persona_activity(_: None = None) -> PersonaResult:
    """Current revision of the default persona."""
    raise NotImplementedError


@activity.defn
async def generate_shitpost_activity(params: GenerateParams) -> GenerateResult:
    """Start (or continue) a whagent session; persona text in the first turn."""
    raise NotImplementedError


@activity.defn
async def check_guardrails_activity(text: str) -> GuardrailResult:
    """Mentions, current-member names and the content filter."""
    raise NotImplementedError


@activity.defn
async def post_and_record_shitpost_activity(params: PostParams) -> ShitpostResult:
    """Re-check the gate, post to Slack, record the post. Never retried."""
    raise NotImplementedError


@activity.defn
async def send_ephemeral_notice_activity(params: NoticeParams) -> None:
    raise NotImplementedError
