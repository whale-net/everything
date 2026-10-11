"""Memory-write guardrails shared by the reflector and the idea-generator.

A generated attribute or lore text is refused when it names a channel member,
reads as an instruction, or matches operator-retired memory.
"""

import re
from typing import Any

from sqlmodel import Session

# Rejection reasons shared by every memory-writing brain job.
REASON_TARGETS_MEMBER = "targets_member"
REASON_INSTRUCTION = "instruction"
REASON_RETIRED_READD = "retired_readd"

# Phrasing that marks an instruction rather than a trait: imperative openers and second-person or directive modals.
INSTRUCTION_RE = re.compile(
    r"^\s*(reply|respond|post|say|use|tell|stop|start|always|never|do|don't|ignore)\b"
    r"|\b(you|your|must|should|from now on)\b",
    re.IGNORECASE,
)


def normalize(text: str) -> str:
    """Case- and whitespace-insensitive form used for retired-text matching."""
    return " ".join(text.lower().split())


def reads_as_instruction(text: str) -> bool:
    return bool(INSTRUCTION_RE.search(text))


def operator_retired_attributes(session: Session, persona_id: int) -> list[dict[str, Any]]:
    """Current operator-retired attributes: {key, text, retired_at}."""
    raise NotImplementedError


def operator_retired_lore(session: Session, persona_id: int) -> list[dict[str, Any]]:
    """Current operator-retired lore entries: {id, text, retired_at}."""
    raise NotImplementedError


def matches_retired(text: str, retired: list[dict[str, Any]]) -> bool:
    """True when the text matches or rewords an operator-retired attribute or lore entry."""
    raise NotImplementedError


def generated_text_refusal(
    text: str, member_names: list[str], retired: list[dict[str, Any]]
) -> str | None:
    """Rejection reason for generated memory text, or None when every check passes."""
    raise NotImplementedError
