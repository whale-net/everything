"""Shitposter persona (SCD2 revisions) and bot post records.

The persona of record lives here, not in whagent-net's agent definition.
Each post records the persona revision it was written from.
"""

import datetime
from enum import Enum

from sqlalchemy import (
    CheckConstraint,
    Column,
    DateTime,
    Index,
    UniqueConstraint,
    func,
    text,
)
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


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
