"""FCM admin set for Shitposter control commands."""

import os


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
