"""Memory-write guardrails shared by the reflector and the idea-generator.

A generated attribute or lore text is refused when it names a channel member,
reads as an instruction, or matches operator-retired memory.
"""

import datetime
import re
from typing import Any

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterAttributeStatusEnum,
    ShitposterLoreEntry,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import SlackUser
from friendly_computing_machine.src.friendly_computing_machine.shitposter_config import (
    load_member_aliases,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter.guardrails import (
    find_member_name,
    find_mention,
)

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

# Word-set overlap at or above which a text counts as a reword of a retired one.
REWORD_SIMILARITY = 0.75
_WORD_RE = re.compile(r"[a-z0-9']+")

# A name token (e.g. a first name) shorter than this, or a common word, is too ambiguous to match alone.
MIN_NAME_TOKEN_LEN = 3
_NAME_TOKEN_RE = re.compile(r"[^\W_]+")
COMMON_WORDS = frozenset(
    """
    the and for are but not you all any can her was one our out day get has him his how man new
    now old see two way who boy did its let put say she too use art bill bob may mark will rich
    joy hope grace faith rose dawn summer autumn april june august sky ray jack pat frank grant
    chase hunter king bishop baker cook carter mason miller page young brown green white black
    gray grey love lucky happy sunny big little real fake cool dude bro guy man men boss chief
    doc dev admin user bot cat dog fox bear wolf bird bee ace max sun moon star angel storm
    official team the mr mrs ms dr jr sr
    """.split()
)


def _utc(dt: datetime.datetime | None) -> datetime.datetime | None:
    # sqlite returns naive datetimes; stored timestamps are UTC
    if dt is None or dt.tzinfo is not None:
        return dt
    return dt.replace(tzinfo=datetime.timezone.utc)


def normalize(text: str) -> str:
    """Case- and whitespace-insensitive form used for retired-text matching."""
    return " ".join(text.lower().split())


def reads_as_instruction(text: str) -> bool:
    return bool(INSTRUCTION_RE.search(text))


def operator_retired_attributes(session: Session, persona_id: int) -> list[dict[str, Any]]:
    """Current operator-retired attributes: {key, text, retired_at}."""
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


def operator_retired_lore(session: Session, persona_id: int) -> list[dict[str, Any]]:
    """Operator-retired lore entries: {id, text, retired_at}."""
    rows = session.exec(
        select(ShitposterLoreEntry).where(
            ShitposterLoreEntry.persona_id == persona_id,
            ShitposterLoreEntry.retired_by_operator.is_(True),  # type: ignore[attr-defined]
        )
    ).all()
    return [{"id": r.id, "text": r.text, "retired_at": _utc(r.valid_to)} for r in rows]


def _words(text: str) -> set[str]:
    return set(_WORD_RE.findall(text.lower()))


def matches_retired(text: str, retired: list[dict[str, Any]]) -> bool:
    """True when the text matches or rewords an operator-retired attribute or lore entry."""
    norm, words = normalize(text), _words(text)
    for r in retired:
        if normalize(r["text"]) == norm:
            return True
        other = _words(r["text"])
        if words and other and len(words & other) / len(words | other) >= REWORD_SIMILARITY:
            return True
    return False


def name_identifiers(name: str | None) -> list[str]:
    """A stored name plus its distinctive tokens (e.g. first and last name)."""
    if not name or not name.strip():
        return []
    out = [name.strip()]
    for token in _NAME_TOKEN_RE.findall(name):
        if len(token) >= MIN_NAME_TOKEN_LEN and token.lower() not in COMMON_WORDS:
            out.append(token)
    return out


def member_identifiers(session: Session) -> list[str]:
    """Every string that identifies a current or former channel member: stored names, their
    distinctive tokens, and operator-configured aliases. Longest first, case-insensitively unique."""
    rows = session.exec(
        select(SlackUser.name, SlackUser.display_name, SlackUser.real_name).where(
            SlackUser.is_bot.is_(False)  # type: ignore[attr-defined]
        )
    ).all()
    seen: dict[str, str] = {}
    candidates = [i for row in rows for n in row for i in name_identifiers(n)]
    for ident in [*candidates, *load_member_aliases()]:
        seen.setdefault(ident.lower(), ident)
    return sorted(seen.values(), key=lambda i: (-len(i), i.lower()))


def generated_text_refusal(
    text: str, member_names: list[str], retired: list[dict[str, Any]]
) -> str | None:
    """Rejection reason for generated memory text, or None when every check passes."""
    if find_mention(text) or find_member_name(text, member_names):
        return REASON_TARGETS_MEMBER
    if reads_as_instruction(text):
        return REASON_INSTRUCTION
    if matches_retired(text, retired):
        return REASON_RETIRED_READD
    return None
