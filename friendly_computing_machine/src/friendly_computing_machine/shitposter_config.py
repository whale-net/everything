"""FCM admin set, engagement, and tunables for Shitposter."""

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


DEFAULT_CONTEXT_TOKEN_BUDGET = 2000
DEFAULT_LORE_DECAY_HALF_LIFE_HOURS = 168.0


def load_context_token_budget() -> int:
    """Read FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET; the snapshot never exceeds it."""
    raw = os.environ.get("FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET", "").strip()
    if not raw:
        return DEFAULT_CONTEXT_TOKEN_BUDGET
    value = int(raw)
    if value <= 0:
        raise ValueError("FCM_SHITPOSTER_CONTEXT_TOKEN_BUDGET must be positive")
    return value


def load_lore_decay_half_life_hours() -> float:
    """Read FCM_SHITPOSTER_LORE_DECAY_HALF_LIFE_HOURS; lore recency halves per period."""
    raw = os.environ.get("FCM_SHITPOSTER_LORE_DECAY_HALF_LIFE_HOURS", "").strip()
    if not raw:
        return DEFAULT_LORE_DECAY_HALF_LIFE_HOURS
    value = float(raw)
    if value <= 0:
        raise ValueError("FCM_SHITPOSTER_LORE_DECAY_HALF_LIFE_HOURS must be positive")
    return value


DEFAULT_SNAPSHOT_RANKED_LORE_CAP = 10


def load_snapshot_ranked_lore_cap() -> int:
    """Read FCM_SHITPOSTER_SNAPSHOT_RANKED_LORE_CAP; lore beyond the top N is random-pick eligible."""
    raw = os.environ.get("FCM_SHITPOSTER_SNAPSHOT_RANKED_LORE_CAP", "").strip()
    if not raw:
        return DEFAULT_SNAPSHOT_RANKED_LORE_CAP
    value = int(raw)
    if value < 1:
        raise ValueError(f"FCM_SHITPOSTER_SNAPSHOT_RANKED_LORE_CAP must be >= 1, got {raw!r}")
    return value


def parse_negative_emoji(raw: str) -> frozenset[str]:
    """Normalize to Slack reaction names: lowercase, surrounding colons stripped."""
    names = (p.strip().strip(":").strip().lower() for p in raw.split(","))
    return frozenset(n for n in names if n)


def load_negative_emoji() -> frozenset[str]:
    """Read FCM_SHITPOSTER_NEGATIVE_EMOJI; empty means no reaction counts as negative."""
    return parse_negative_emoji(os.environ.get("FCM_SHITPOSTER_NEGATIVE_EMOJI", ""))


DEFAULT_LORE_TOP_SHARE = 0.10
DEFAULT_LORE_WINDOW_DAYS = 30


def load_lore_top_share() -> float:
    """Read FCM_SHITPOSTER_LORE_TOP_SHARE (fraction in (0, 1]); default 0.10."""
    raw = os.environ.get("FCM_SHITPOSTER_LORE_TOP_SHARE", "").strip()
    if not raw:
        return DEFAULT_LORE_TOP_SHARE
    share = float(raw)
    if not 0 < share <= 1:
        raise ValueError(f"FCM_SHITPOSTER_LORE_TOP_SHARE must be in (0, 1], got {raw!r}")
    return share


def load_lore_window_days() -> int:
    """Read FCM_SHITPOSTER_LORE_WINDOW_DAYS (positive int); default 30."""
    raw = os.environ.get("FCM_SHITPOSTER_LORE_WINDOW_DAYS", "").strip()
    if not raw:
        return DEFAULT_LORE_WINDOW_DAYS
    days = int(raw)
    if days < 1:
        raise ValueError(f"FCM_SHITPOSTER_LORE_WINDOW_DAYS must be >= 1, got {raw!r}")
    return days


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
