"""FCM admin set, tunables, and engagement config for Shitposter control commands."""

import logging
import os
from dataclasses import dataclass

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


def parse_member_aliases(raw: str) -> list[str]:
    return [p.strip() for p in raw.split(",") if p.strip()]


def load_member_aliases() -> list[str]:
    """Read FCM_SHITPOSTER_MEMBER_ALIASES: extra nicknames that identify a channel member."""
    return parse_member_aliases(os.environ.get("FCM_SHITPOSTER_MEMBER_ALIASES", ""))


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


DEFAULT_REFLECTOR_INPUT_CAP = 200
DEFAULT_ATTRIBUTE_CAP = 20


def reflector_input_cap() -> int:
    """Engagement and promoted-suggestion inputs one reflect run reads, newest first (FCM_SHITPOSTER_REFLECTOR_INPUT_CAP)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_REFLECTOR_INPUT_CAP", DEFAULT_REFLECTOR_INPUT_CAP
    )


def attribute_cap() -> int:
    """Maximum active persona attributes after a reflect run (FCM_SHITPOSTER_ATTRIBUTE_CAP)."""
    return _positive_int_env("FCM_SHITPOSTER_ATTRIBUTE_CAP", DEFAULT_ATTRIBUTE_CAP)


DEFAULT_WRITE_CADENCE_HOURS = 3
DEFAULT_SNAPSHOT_CADENCE_HOURS = 24
DEFAULT_DRAFT_BATCH_SIZE = 5
DEFAULT_DRAFT_EXPIRY_HOURS = 24


def write_cadence_hours() -> int:
    """Hours between writer brain job runs (FCM_SHITPOSTER_WRITE_CADENCE_HOURS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_WRITE_CADENCE_HOURS", DEFAULT_WRITE_CADENCE_HOURS
    )


def snapshot_cadence_hours() -> int:
    """Hours between scheduled snapshot brain job runs (FCM_SHITPOSTER_SNAPSHOT_CADENCE_HOURS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_SNAPSHOT_CADENCE_HOURS", DEFAULT_SNAPSHOT_CADENCE_HOURS
    )


def draft_batch_size() -> int:
    """Drafts requested per writer run (FCM_SHITPOSTER_DRAFT_BATCH_SIZE)."""
    return _positive_int_env("FCM_SHITPOSTER_DRAFT_BATCH_SIZE", DEFAULT_DRAFT_BATCH_SIZE)


def draft_expiry_hours() -> int:
    """Age after which a queued draft is no longer eligible for a scheduled post (FCM_SHITPOSTER_DRAFT_EXPIRY_HOURS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_DRAFT_EXPIRY_HOURS", DEFAULT_DRAFT_EXPIRY_HOURS
    )


THEME_GENERATOR_AGENT_DEFINITION = "shitposter-theme-generator"
NOVEL_GENERATOR_AGENT_DEFINITION = "shitposter-novel-generator"
DEFAULT_IDEA_GENERATOR_RUN_CAP = 2
DEFAULT_IDEA_GENERATOR_CADENCE_HOURS = 24
DEFAULT_THEME_TARGET_WEIGHT = 0.15
DEFAULT_NOVEL_TARGET_WEIGHT = 0.10
DEFAULT_SOURCE_WEIGHT_MIN = 0.05
DEFAULT_SOURCE_WEIGHT_MAX = 0.30
DEFAULT_SOURCE_WEIGHT_CEILING = 0.40
DEFAULT_NOVEL_GATE_RECENT_DAYS = 7
DEFAULT_NOVEL_GATE_BASELINE_DAYS = 30
DEFAULT_NOVEL_GATE_FRACTION = 0.8
DEFAULT_WEIGHT_STEP = 0.01
DEFAULT_WEIGHT_WINDOW_DAYS = 30
DEFAULT_WEIGHT_MIN_POSTS = 10


def load_theme_generator_agent_id() -> str:
    """Agent definition for theme synthesis; FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID overrides it."""
    override = os.environ.get("FCM_SHITPOSTER_THEME_GENERATOR_AGENT_ID", "").strip()
    return override or THEME_GENERATOR_AGENT_DEFINITION


def load_novel_generator_agent_id() -> str:
    """Agent definition for novel exploration; FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID overrides it."""
    override = os.environ.get("FCM_SHITPOSTER_NOVEL_GENERATOR_AGENT_ID", "").strip()
    return override or NOVEL_GENERATOR_AGENT_DEFINITION


def idea_generator_run_cap() -> int:
    """Most theme plus novel lore entries one idea-generator run writes (FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_IDEA_GENERATOR_RUN_CAP", DEFAULT_IDEA_GENERATOR_RUN_CAP
    )


def idea_generator_cadence_hours() -> int | None:
    """Hours between scheduled idea-generator runs; None when FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS is "off"."""
    name = "FCM_SHITPOSTER_IDEA_GENERATOR_CADENCE_HOURS"
    if os.environ.get(name, "").strip().lower() == "off":
        return None
    return _positive_int_env(name, DEFAULT_IDEA_GENERATOR_CADENCE_HOURS)


def _unit_fraction_env(name: str, default: float) -> float:
    raw = os.environ.get(name, "").strip()
    if not raw:
        return default
    try:
        value = float(raw)
    except ValueError:
        value = -1.0
    if not 0 < value <= 1:
        logger.warning("%s=%r is not a fraction in (0, 1]; using %s", name, raw, default)
        return default
    return value


def theme_target_weight() -> float:
    """Starting theme target weight (FCM_SHITPOSTER_THEME_TARGET_WEIGHT); stored weights take over after seeding."""
    return _unit_fraction_env(
        "FCM_SHITPOSTER_THEME_TARGET_WEIGHT", DEFAULT_THEME_TARGET_WEIGHT
    )


def novel_target_weight() -> float:
    """Starting novel target weight (FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT); stored weights take over after seeding."""
    return _unit_fraction_env(
        "FCM_SHITPOSTER_NOVEL_TARGET_WEIGHT", DEFAULT_NOVEL_TARGET_WEIGHT
    )


@dataclass(frozen=True)
class SourceWeightConfig:
    """Starting weights, per-source bounds and combined ceiling for theme/novel weights."""

    theme_start: float
    novel_start: float
    theme_min: float
    theme_max: float
    novel_min: float
    novel_max: float
    ceiling: float
    # True when configured starting weights were rejected for the defaults
    fell_back: bool = False


def _bounds_valid(lo: float, hi: float) -> bool:
    return 0 < lo <= hi <= 1


def load_source_weight_config() -> SourceWeightConfig:
    """Source-weight config; invalid starting weights fall back to the defaults with a warning.

    Bounds come from FCM_SHITPOSTER_{THEME,NOVEL}_WEIGHT_{MIN,MAX} and the ceiling from
    FCM_SHITPOSTER_SOURCE_WEIGHT_CEILING; the starting weights are the *_TARGET_WEIGHT vars.
    """
    theme_min = _unit_fraction_env("FCM_SHITPOSTER_THEME_WEIGHT_MIN", DEFAULT_SOURCE_WEIGHT_MIN)
    theme_max = _unit_fraction_env("FCM_SHITPOSTER_THEME_WEIGHT_MAX", DEFAULT_SOURCE_WEIGHT_MAX)
    novel_min = _unit_fraction_env("FCM_SHITPOSTER_NOVEL_WEIGHT_MIN", DEFAULT_SOURCE_WEIGHT_MIN)
    novel_max = _unit_fraction_env("FCM_SHITPOSTER_NOVEL_WEIGHT_MAX", DEFAULT_SOURCE_WEIGHT_MAX)
    ceiling = _unit_fraction_env(
        "FCM_SHITPOSTER_SOURCE_WEIGHT_CEILING", DEFAULT_SOURCE_WEIGHT_CEILING
    )
    if not (
        _bounds_valid(theme_min, theme_max)
        and _bounds_valid(novel_min, novel_max)
        and theme_min + novel_min <= ceiling
    ):
        logger.warning(
            "source weight bounds theme %s-%s, novel %s-%s, ceiling %s are inconsistent; "
            "using defaults",
            theme_min, theme_max, novel_min, novel_max, ceiling,
        )
        theme_min = novel_min = DEFAULT_SOURCE_WEIGHT_MIN
        theme_max = novel_max = DEFAULT_SOURCE_WEIGHT_MAX
        ceiling = DEFAULT_SOURCE_WEIGHT_CEILING

    theme_start, novel_start = theme_target_weight(), novel_target_weight()
    fell_back = False
    if not (
        theme_min <= theme_start <= theme_max
        and novel_min <= novel_start <= novel_max
        and theme_start + novel_start <= ceiling + 1e-9
    ):
        logger.warning(
            "starting source weights theme %s, novel %s fall outside bounds "
            "(theme %s-%s, novel %s-%s) or exceed ceiling %s; using defaults",
            theme_start, novel_start, theme_min, theme_max, novel_min, novel_max, ceiling,
        )
        fell_back = True
        # clamp the defaults too, in case the configured bounds exclude them
        theme_start = min(max(DEFAULT_THEME_TARGET_WEIGHT, theme_min), theme_max)
        novel_start = min(max(DEFAULT_NOVEL_TARGET_WEIGHT, novel_min), novel_max)
        if theme_start + novel_start > ceiling + 1e-9:
            theme_start, novel_start = theme_min, novel_min
    return SourceWeightConfig(
        theme_start=theme_start,
        novel_start=novel_start,
        theme_min=theme_min,
        theme_max=theme_max,
        novel_min=novel_min,
        novel_max=novel_max,
        ceiling=ceiling,
        fell_back=fell_back,
    )


def novel_gate_recent_days() -> int:
    """Recent window of the novel gate, in days (FCM_SHITPOSTER_NOVEL_GATE_RECENT_DAYS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_NOVEL_GATE_RECENT_DAYS", DEFAULT_NOVEL_GATE_RECENT_DAYS
    )


def novel_gate_baseline_days() -> int:
    """Baseline window of the novel gate, in days (FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_NOVEL_GATE_BASELINE_DAYS", DEFAULT_NOVEL_GATE_BASELINE_DAYS
    )


def novel_gate_fraction() -> float:
    """Gate opens when the recent mean is below this fraction of the baseline mean (FCM_SHITPOSTER_NOVEL_GATE_FRACTION)."""
    return _unit_fraction_env(
        "FCM_SHITPOSTER_NOVEL_GATE_FRACTION", DEFAULT_NOVEL_GATE_FRACTION
    )


def weight_step() -> float:
    """Size of one source-weight step, as a fraction (FCM_SHITPOSTER_WEIGHT_STEP)."""
    return _unit_fraction_env("FCM_SHITPOSTER_WEIGHT_STEP", DEFAULT_WEIGHT_STEP)


def weight_window_days() -> int:
    """Trailing comparison window, in days, for source-weight adaptation (FCM_SHITPOSTER_WEIGHT_WINDOW_DAYS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_WEIGHT_WINDOW_DAYS", DEFAULT_WEIGHT_WINDOW_DAYS
    )


def weight_min_posts() -> int:
    """Posts each group needs before a source weight moves (FCM_SHITPOSTER_WEIGHT_MIN_POSTS)."""
    return _positive_int_env(
        "FCM_SHITPOSTER_WEIGHT_MIN_POSTS", DEFAULT_WEIGHT_MIN_POSTS
    )
