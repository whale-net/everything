"""FCM admin set, engagement, and tunables for Shitposter."""

import logging
import os

logger = logging.getLogger(__name__)

DEFAULT_SUGGESTION_DAILY_LIMIT = 3
DEFAULT_SUGGESTION_EXPIRY_DAYS = 7


def parse_admin_slack_user_ids(raw: str) -> frozenset[str]:
    return frozenset(p.strip() for p in raw.split(",") if p.strip())


def load_admin_slack_user_ids() -> frozenset[str]:
    """Read FCM_ADMIN_SLACK_USER_IDS; empty means no admins."""
    return parse_admin_slack_user_ids(os.environ.get("FCM_ADMIN_SLACK_USER_IDS", ""))


def parse_negative_emoji(raw: str) -> frozenset[str]:
    """Normalize to Slack reaction names: lowercase, surrounding colons stripped."""
    names = (p.strip().strip(":").strip().lower() for p in raw.split(","))
    return frozenset(n for n in names if n)


def load_negative_emoji() -> frozenset[str]:
    """Read FCM_SHITPOSTER_NEGATIVE_EMOJI; empty means no reaction counts as negative."""
    return parse_negative_emoji(os.environ.get("FCM_SHITPOSTER_NEGATIVE_EMOJI", ""))


def _positive_int_env(name: str, default: int) -> int:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = int(raw)
    except ValueError:
        value = 0
    if value < 1:
        logger.warning("%s=%r is not a positive integer; using %d", name, raw, default)
        return default
    return value


def suggestion_daily_limit() -> int:
    """Persona suggestions one member may submit per trailing 24h (FCM_SHITPOSTER_SUGGESTION_DAILY_LIMIT)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_SUGGESTION_DAILY_LIMIT", DEFAULT_SUGGESTION_DAILY_LIMIT
    )


def suggestion_expiry_days() -> int:
    """Days a pending suggestion waits for backing before it expires (FCM_SHITPOSTER_SUGGESTION_EXPIRY_DAYS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_SUGGESTION_EXPIRY_DAYS", DEFAULT_SUGGESTION_EXPIRY_DAYS
    )
