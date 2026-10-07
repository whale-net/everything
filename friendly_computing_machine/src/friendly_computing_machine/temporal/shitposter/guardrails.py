"""Pure Shitposter output guardrails: mentions, member names, content filter."""

import os
import re
from typing import Iterable, Optional

# <@U123>, <@U123|name>, <!here>, <!channel>, <!everyone>, <!subteam^S123>
MENTION_RE = re.compile(r"<@[UW][A-Z0-9]+(?:\|[^>]*)?>|<!(?:here|channel|everyone|subteam\^[A-Z0-9]+)(?:\|[^>]*)?>")

# Always-on denylist; extend per deployment via FCM_SHITPOSTER_DENYLIST.
_BUILTIN_DENYLIST = ("kys", "kill yourself")


def find_mention(text: str) -> Optional[str]:
    match = MENTION_RE.search(text)
    return match.group(0) if match else None


def find_member_name(text: str, names: Iterable[str]) -> Optional[str]:
    """First current-member name appearing as a whole word, case-insensitive."""
    for name in names:
        name = name.strip()
        if len(name) < 2:
            continue
        pattern = r"(?<!\w)" + re.escape(name) + r"(?!\w)"
        if re.search(pattern, text, re.IGNORECASE):
            return name
    return None


def _denylist() -> list[str]:
    extra = os.environ.get("FCM_SHITPOSTER_DENYLIST", "")
    return [*_BUILTIN_DENYLIST, *(t.strip().lower() for t in extra.split(",") if t.strip())]


def passes_content_filter(text: str) -> bool:
    """The single swappable content-filter seam; True means safe to post."""
    lowered = text.lower()
    for term in _denylist():
        if re.search(r"(?<!\w)" + re.escape(term) + r"(?!\w)", lowered):
            return False
    return True


def output_refusal(text: str, member_names: Iterable[str]) -> Optional[str]:
    """Reason code the text must not be posted, or None when every check passes."""
    if not text.strip():
        return "empty"
    if find_mention(text):
        return "mention"
    if find_member_name(text, member_names):
        return "member_name"
    if not passes_content_filter(text):
        return "content_filter"
    return None
