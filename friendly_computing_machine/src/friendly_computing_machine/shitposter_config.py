"""FCM admin set and engagement config for Shitposter."""

import os


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


def load_lore_top_share() -> float:
    """Read FCM_SHITPOSTER_LORE_TOP_SHARE (fraction in (0, 1]); default 0.10."""
    raise NotImplementedError


def load_lore_window_days() -> int:
    """Read FCM_SHITPOSTER_LORE_WINDOW_DAYS (positive int); default 30."""
    raise NotImplementedError
