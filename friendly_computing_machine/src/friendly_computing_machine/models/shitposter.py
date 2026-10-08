"""Shitposter records: control plane (SCD2 opt-in, kill switch), persona
(SCD2 revisions) and bot post records.

Channel opt-in is a dedicated per-channel record, never a slackchannel column.
The kill switch is a workspace-wide singleton; with no current row Shitposter
is ON. The persona of record lives here, not in whagent-net's agent
definition; each post records the persona revision it was written from.
"""

import datetime
from enum import Enum

from sqlalchemy import (
    CheckConstraint,
    Column,
    DateTime,
    Index,
    JSON,
    UniqueConstraint,
    func,
    text,
)
from sqlalchemy.dialects import postgresql
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
# registers the snapshot table that ShitposterPost.context_snapshot_id references
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (  # noqa: F401
    ShitposterContextSnapshot,
)

# the kill switch's singleton key
KILL_SWITCH_SCOPE = "workspace"


class ShitposterChannelOptInBase(Base):
    slack_channel_id: int = Field(foreign_key="slackchannel.id", index=True)
    opted_in: bool
    set_by_slack_user_id: str
    set_by_slack_team_id: str | None = Field(default=None, nullable=True)


class ShitposterChannelOptIn(ShitposterChannelOptInBase, table=True):
    __table_args__ = (
        # at most one current row per channel
        Index(
            "uq_shitposterchanneloptin_current",
            "slack_channel_id",
            unique=True,
            postgresql_where=text("valid_to IS NULL"),
            sqlite_where=text("valid_to IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    valid_from: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    valid_to: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterKillSwitchBase(Base):
    scope: str = Field(default=KILL_SWITCH_SCOPE)
    # true = Shitposter ON
    enabled: bool
    # a Slack user id, or a free-text actor for out-of-band writes
    set_by: str
    reason: str | None = Field(default=None, nullable=True)


class ShitposterKillSwitch(ShitposterKillSwitchBase, table=True):
    __table_args__ = (
        Index(
            "uq_shitposterkillswitch_current",
            "scope",
            unique=True,
            postgresql_where=text("valid_to IS NULL"),
            sqlite_where=text("valid_to IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    valid_from: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    valid_to: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterTriggerEnum(str, Enum):
    SCHEDULED = "scheduled"
    SUMMON = "summon"
    RIFF = "riff"


class ShitposterPrincipalKindEnum(str, Enum):
    SERVICE = "service"
    HUMAN = "human"


class ShitposterPersona(Base, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    name: str
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )


class ShitposterPersonaRevision(Base, table=True):
    __table_args__ = (
        # at most one current revision per persona
        Index(
            "uq_shitposterpersonarevision_current",
            "persona_id",
            unique=True,
            postgresql_where=text("valid_to IS NULL"),
            sqlite_where=text("valid_to IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    # the fixed persona prompt FCM supplies as the session's first turn
    persona_text: str
    # why this revision exists; M7 cites feedback/suggestion causes here
    cause_kind: str | None = Field(default=None, nullable=True)
    cause_ref: str | None = Field(default=None, nullable=True)
    valid_from: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )
    valid_to: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterPostBase(Base):
    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )
    # Slack ts of the bot post; joins to captured reactions by (channel, ts)
    slack_message_ts: str
    # set for riff replies
    thread_ts: str | None = Field(default=None, nullable=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    persona_revision_id: int = Field(
        nullable=False, foreign_key="shitposterpersonarevision.id", index=True
    )
    # ShitposterTriggerEnum value
    trigger: str
    # the subject the whagent turn ran as / on behalf of
    principal_iss: str
    principal_sub: str
    # ShitposterPrincipalKindEnum value
    principal_kind: str
    whagent_session_id: str
    # the summoner, or the first linked human replier of a scheduled post
    thread_owner_slack_user_id: str | None = Field(default=None, nullable=True)
    # riff -> root post
    parent_post_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterpost.id", index=True
    )


class ShitposterScheduledSkipReasonEnum(str, Enum):
    WRITER_UNAVAILABLE = "writer_unavailable"


class ShitposterScheduledSkip(Base, table=True):
    """A scheduled slot that produced no post because the writer was unavailable."""

    __table_args__ = (
        CheckConstraint(
            "reason IN ('writer_unavailable')",
            name="ck_shitposterscheduledskip_reason",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )
    # ShitposterScheduledSkipReasonEnum value
    reason: str
    # snapshot the slot would have written from, if one existed when it skipped
    context_snapshot_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitpostercontextsnapshot.id"
    )
    skipped_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )


class ShitposterPost(ShitposterPostBase, table=True):
    __table_args__ = (
        UniqueConstraint(
            "slack_channel_id",
            "slack_message_ts",
            name="uq_shitposterpost_channel_ts",
        ),
        CheckConstraint(
            "trigger IN ('scheduled', 'summon', 'riff')",
            name="ck_shitposterpost_trigger",
        ),
        CheckConstraint(
            "principal_kind IN ('service', 'human')",
            name="ck_shitposterpost_principal_kind",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )
    # snapshot the post was written from; NULL only when no snapshot existed yet
    context_snapshot_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitpostercontextsnapshot.id", index=True
    )


class ShitposterPostEngagement(Base, table=True):
    """Write-once engagement record for a bot post, written after its 24h window."""

    post_id: int = Field(
        primary_key=True, foreign_key="shitposterpost.id", nullable=False
    )
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    finalized_at: datetime.datetime = Field(
        sa_column=Column(DateTime(timezone=True), nullable=False, server_default=func.now()),
    )
    distinct_reactors: int
    # emoji name -> count of distinct human reactions
    reactions_by_emoji: dict = Field(
        sa_column=Column(JSON().with_variant(postgresql.JSONB(), "postgresql"), nullable=False),
    )
    distinct_repliers: int
    # reactions whose emoji is on FCM_SHITPOSTER_NEGATIVE_EMOJI, counted separately
    negative_reactions: int
    consumed_by_reflector_run_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterbrainjobrun.id"
    )
    consumed_by_lore_run_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterbrainjobrun.id"
    )


class ShitposterSuggestionStatusEnum(str, Enum):
    PENDING = "pending"
    PROMOTED = "promoted"
    APPLIED = "applied"
    DECLINED = "declined"
    EXPIRED = "expired"


class ShitposterSuggestion(Base, table=True):
    """A community persona suggestion posted as a pending message in a channel."""

    __table_args__ = (
        UniqueConstraint(
            "slack_channel_id",
            "slack_message_ts",
            name="uq_shitpostersuggestion_channel_ts",
        ),
        CheckConstraint(
            "status IN ('pending', 'promoted', 'applied', 'declined', 'expired')",
            name="ck_shitpostersuggestion_status",
        ),
        Index(
            "ix_shitpostersuggestion_status_expires_at",
            "status",
            "expires_at",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )
    # Slack ts of the posted pending-suggestion message; reactions join on (channel, ts)
    slack_message_ts: str
    submitter_slack_user_id: str
    text: str
    # ShitposterSuggestionStatusEnum value
    status: str = Field(default=ShitposterSuggestionStatusEnum.PENDING.value)
    submitted_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )
    expires_at: datetime.datetime = Field(
        sa_column=Column(DateTime(timezone=True), nullable=False),
    )
    status_changed_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )
    promoted_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    # run id of the reflector run that consumed this promoted suggestion; NULL until consumed
    consumed_by_reflector_run_id: str | None = Field(default=None, nullable=True)


class ShitposterSuggestionBacker(Base, table=True):
    """A human Slack user backing a pending suggestion; removal stamps removed_at.

    The submitter is not a row here; it counts once toward the threshold implicitly.
    """

    __table_args__ = (
        Index(
            "uq_shitpostersuggestionbacker_active",
            "suggestion_id",
            "slack_user_id",
            unique=True,
            postgresql_where=text("removed_at IS NULL"),
            sqlite_where=text("removed_at IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    suggestion_id: int = Field(
        nullable=False, foreign_key="shitpostersuggestion.id", index=True
    )
    slack_user_id: str
    backed_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )
    removed_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterSuggestionOutcomeKindEnum(str, Enum):
    APPLIED = "applied"
    DECLINED = "declined"
    EXPIRED = "expired"


class ShitposterSuggestionCoarseReasonEnum(str, Enum):
    OFF_TOPIC = "off_topic"
    UNSAFE = "unsafe"
    DUPLICATE = "duplicate"
    OTHER = "other"


class ShitposterSuggestionReplyOutbox(Base, table=True):
    """Pending thread replies on a suggestion post; drained only while the kill switch is off."""

    __table_args__ = (
        CheckConstraint(
            "kind IN ('applied', 'declined', 'expired')",
            name="ck_shitpostersuggestionreplyoutbox_kind",
        ),
        CheckConstraint(
            "coarse_reason IS NULL OR coarse_reason IN "
            "('off_topic', 'unsafe', 'duplicate', 'other')",
            name="ck_shitpostersuggestionreplyoutbox_coarse_reason",
        ),
        Index(
            "ix_shitpostersuggestionreplyoutbox_unposted",
            "created_at",
            postgresql_where=text("posted_at IS NULL"),
            sqlite_where=text("posted_at IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    suggestion_id: int = Field(
        nullable=False, foreign_key="shitpostersuggestion.id", index=True
    )
    # ShitposterSuggestionOutcomeKindEnum value
    kind: str
    # coarse, fixed vocabulary; never carries the declined text or a backer name
    coarse_reason: str | None = Field(default=None, nullable=True)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.now(),
        ),
    )
    posted_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterBrainJobKind(str, Enum):
    HARVEST = "harvest"
    REFLECT = "reflect"
    WRITE = "write"
    SNAPSHOT = "snapshot"


class ShitposterBrainJobTrigger(str, Enum):
    SCHEDULE = "schedule"
    OPERATOR = "operator"


class ShitposterBrainJobStatus(str, Enum):
    RUNNING = "running"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    SKIPPED = "skipped"
    NO_OP = "no_op"


class ShitposterBrainJobRun(Base, table=True):
    """Append-only record of each brain job attempt, including skips."""

    __table_args__ = (
        # at most one running job per persona; the job runner's lock
        Index(
            "uq_shitposterbrainjobrun_running",
            "persona_id",
            unique=True,
            postgresql_where=text("status = 'running'"),
            sqlite_where=text("status = 'running'"),
        ),
        CheckConstraint(
            "job_kind IN ('harvest', 'reflect', 'write', 'snapshot')",
            name="ck_shitposterbrainjobrun_job_kind",
        ),
        CheckConstraint(
            "trigger IN ('schedule', 'operator')",
            name="ck_shitposterbrainjobrun_trigger",
        ),
        CheckConstraint(
            "status IN ('running', 'succeeded', 'failed', 'skipped', 'no_op')",
            name="ck_shitposterbrainjobrun_status",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    # ShitposterBrainJobKind value
    job_kind: str
    # ShitposterBrainJobTrigger value
    trigger: str
    # ShitposterBrainJobStatus value
    status: str
    started_at: datetime.datetime = Field(
        sa_column=Column(DateTime(timezone=True), nullable=False, server_default=func.now()),
    )
    finished_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    skip_reason: str | None = Field(default=None, nullable=True)
    error: str | None = Field(default=None, nullable=True)
    details: dict | None = Field(
        default=None, sa_column=Column(
            JSON().with_variant(postgresql.JSONB(), "postgresql"), nullable=True
        )
    )
