"""FCM admin set and tunables for Shitposter control commands."""

import logging
import os

logger = logging.getLogger(__name__)

DEFAULT_SUGGESTION_DAILY_LIMIT = 3
DEFAULT_SUGGESTION_EXPIRY_DAYS = 7
DEFAULT_SUGGESTION_BACKER_THRESHOLD = 3


def parse_admin_slack_user_ids(raw: str) -> frozenset[str]:
    return frozenset(p.strip() for p in raw.split(",") if p.strip())


def load_admin_slack_user_ids() -> frozenset[str]:
    """Read FCM_ADMIN_SLACK_USER_IDS; empty means no admins."""
    return parse_admin_slack_user_ids(os.environ.get("FCM_ADMIN_SLACK_USER_IDS", ""))


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


def suggestion_backer_threshold() -> int:
    """Distinct human backers (submitter included) a suggestion needs to be promoted (FCM_SHITPOSTER_SUGGESTION_BACKER_THRESHOLD)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_SUGGESTION_BACKER_THRESHOLD", DEFAULT_SUGGESTION_BACKER_THRESHOLD
    )
