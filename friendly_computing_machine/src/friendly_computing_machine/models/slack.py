import datetime
from enum import Enum
from typing import Any, Dict, Optional

import sqlalchemy as sa
from sqlalchemy import Boolean, Column, DateTime, UniqueConstraint, func
from sqlmodel import Field, Relationship

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.util import (
    ts_to_datetime,
)


# -----
# team
class SlackTeamBase(Base):
    slack_id: str = Field(index=True, unique=True)
    name: str


class SlackTeam(SlackTeamBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)


# read-only pydantic? unsure of the benefit of this
# I think it's so that any extra creation info can be contained in this class rather in the base class
# going to try it out
class SlackTeamCreate(SlackTeamBase):
    def to_slack_team(self) -> SlackTeam:
        return SlackTeam(
            slack_id=self.slack_id,
            name=self.name,
        )


# -----
# user
class SlackUserBase(Base):
    slack_id: str = Field(index=True, unique=True)
    name: str
    is_bot: bool = False
    slack_team_slack_id: str = Field(nullable=True)


class SlackUser(SlackUserBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    slack_team_id: int = Field(nullable=True, foreign_key="slackteam.id", index=True)


class SlackUserCreate(SlackUserBase):
    def to_slack_user(self, slack_team_id: Optional[int] = None) -> SlackUser:
        # TBD if slack_team_id should be non-optional
        return SlackUser(
            slack_id=self.slack_id,
            name=self.name,
            is_bot=self.is_bot,
            slack_team_slack_id=self.slack_team_slack_id,
            slack_team_id=slack_team_id,
        )


# -----
# message
class SlackMessageBase(Base):
    slack_id: str | None = Field(index=True)
    slack_team_slack_id: str
    slack_channel_slack_id: str
    slack_user_slack_id: str
    text: str
    ts: datetime.datetime
    # if not null, is thread
    thread_ts: datetime.datetime | None
    # unsure how to make sense of this since I am testing with only one user - but seems useful
    parent_user_slack_id: str | None


class SlackMessage(SlackMessageBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    # time that this message was processed and updated
    processed_date: datetime.datetime | None
    slack_team_id: int = Field(nullable=True, foreign_key="slackteam.id", index=True)
    slack_channel_id: int = Field(
        nullable=True, foreign_key="slackchannel.id", index=True
    )
    slack_user_id: int = Field(nullable=True, foreign_key="slackuser.id", index=True)
    slack_parent_user_id: int = Field(
        nullable=True, foreign_key="slackuser.id", index=True
    )


class SlackMessageCreate(SlackMessageBase):
    @classmethod
    def from_slack_message_json(
        cls,
        message_event: Dict[str, Any],
        slack_channel_slack_id: Optional[str] = None,
        team_slack_id: Optional[str] = None,
    ) -> "SlackMessageCreate":
        thread_ts = message_event.get("thread_ts")
        channel_id = message_event.get("channel", slack_channel_slack_id)
        if channel_id is None:
            raise ValueError("channel_id cannot be None")
        team_id = message_event.get("team") or team_slack_id
        if team_id is None:
            raise ValueError("team_id cannot be None")
        message = SlackMessageCreate(
            slack_id=message_event.get("client_msg_id"),
            slack_team_slack_id=team_id,
            slack_channel_slack_id=channel_id,
            slack_user_slack_id=message_event.get("user"),
            text=message_event.get("text"),
            ts=ts_to_datetime(message_event.get("ts")),
            thread_ts=ts_to_datetime(thread_ts) if thread_ts else None,
            parent_user_slack_id=message_event.get("parent_user_id"),
        )
        return message


# -----
# channel
class SlackChannelBase(Base):
    slack_id: str = Field(index=True, unique=True)
    name: str
    channel_type: str
    # TODO deprecated
    is_music_poll: bool = Field(default=False)


class SlackChannel(SlackChannelBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)


class SlackChannelCreate(SlackChannelBase):
    pass


# ------
# commands
class SlackCommandBase(Base):
    caller_slack_user_id: str = Field(index=True)
    command_base: str
    command_text: str
    slack_channel_slack_id: Optional[str]
    created_at: datetime.datetime


class SlackCommand(SlackCommandBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    # TODO - add slack_channel_id
    slack_channel_id: Optional[int] = Field(
        nullable=True, foreign_key="slackchannel.id", index=True
    )
    slack_user_id: Optional[int] = Field(
        nullable=True, foreign_key="slackuser.id", index=True
    )

    @classmethod
    def from_slack_command_create(cls, create: "SlackCommandCreate") -> "SlackCommand":
        return cls(
            caller_slack_user_id=create.caller_slack_user_id,
            command_base=create.command_base,
            command_text=create.command_text,
            slack_channel_slack_id=create.slack_channel_slack_id,
            created_at=create.created_at,
        )


class SlackCommandCreate(SlackCommandBase):
    pass


# ------
# slack special channels


# TODO - reference this enum in thhe migrations when appropriate
# but don't make alemibc maintain it because that is probably not a good idea
class SlackSpecialChannelTypeEnum(Enum):
    """Corresponds to the SlackSpecialChannelType table's type_name field."""

    MANMAN_DEV = "manman_dev"


class SlackSpecialChannelTypeBase(Base):
    type_name: str = Field(index=True, unique=True)
    friendly_type_name: str


class SlackSpecialChannelType(SlackSpecialChannelTypeBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)


class SlackSpecialChannelTypeCreate(SlackSpecialChannelTypeBase):
    pass


class SlackSpecialChannelBase(Base):
    reason: Optional[str] = None

    # no updated at wahtever
    enabled: bool = True


class SlackSpecialChannel(SlackSpecialChannelBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)

    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )

    slack_special_channel_type_id: int = Field(
        nullable=False, foreign_key="slackspecialchanneltype.id", index=True
    )

    slack_special_channel_type: SlackSpecialChannelType = Relationship()
    slack_channel: SlackChannel = Relationship()


class SlackSpecialChannelCreate(SlackSpecialChannelBase):
    pass


# ------
# slack channel -> whagent-net agent link
#
# maps a Slack channel to the whagent-net agent definition that a new session
# started in that channel should use. hand-inserted via SQL for now, no
# admin UI/command yet.


class SlackChannelAgentLinkBase(Base):
    # whagent-net's agent_id string (e.g. "audience-score-system-research").
    # whagent-net's own agent_definition table is out of this repo's reach,
    # so this is just a plain string column, not a FK.
    whagent_agent_id: str

    enabled: bool = True


class SlackChannelAgentLink(SlackChannelAgentLinkBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)

    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )

    slack_channel: SlackChannel = Relationship()


class SlackChannelAgentLinkCreate(SlackChannelAgentLinkBase):
    pass


# ------
# slack thread -> whagent-net session
#
# tracks which active Slack thread maps to which whagent-net session, so a
# reply landing in an existing thread continues that session instead of
# starting a new one. read/written by a per-thread Temporal workflow.


class SlackThreadSessionStatusEnum(Enum):
    """Corresponds to the SlackThreadSession table's status field."""

    ACTIVE = 0
    CLOSED = 1


class SlackThreadSessionBase(Base):
    thread_ts: str = Field(index=True)

    # whagent-net's session_id string
    whagent_session_id: str

    status: SlackThreadSessionStatusEnum = Field(
        default=SlackThreadSessionStatusEnum.ACTIVE
    )


class SlackThreadSession(SlackThreadSessionBase, table=True):
    __table_args__ = (
        UniqueConstraint(
            "slack_channel_id", "thread_ts", name="uq_slackthreadsession_channel_thread"
        ),
    )

    id: int = Field(default=None, nullable=False, primary_key=True)

    slack_channel_id: int = Field(
        nullable=False, foreign_key="slackchannel.id", index=True
    )

    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )
    updated_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
            onupdate=func.current_timestamp(),
        ),
    )

    slack_channel: SlackChannel = Relationship()


class SlackThreadSessionCreate(SlackThreadSessionBase):
    pass


# ------
# slack identity -> keycloak identity
#
# one row per Slack user, holding only the keycloak (iss, sub) pair that user
# linked. Slack ids are the native Slack strings, not fcm slackteam/slackuser
# row ids, so linking works for a user the periodic sync hasn't seen yet.


class SlackKeycloakIdentityBase(Base):
    # slack's native team id (T...) and user id (U...) strings
    slack_team_id: str = Field(index=True)
    slack_user_id: str = Field(index=True)

    # keycloak token issuer + subject. no keycloak tokens are ever stored.
    keycloak_iss: str
    keycloak_sub: str


class SlackKeycloakIdentity(SlackKeycloakIdentityBase, table=True):
    # re-linking a user overwrites their mapping rather than stacking rows
    __table_args__ = (
        UniqueConstraint(
            "slack_team_id", "slack_user_id", name="uq_slackkeycloakidentity_slack_user"
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
    updated_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
            onupdate=func.current_timestamp(),
        ),
    )


# ------
# one-time slack -> keycloak link token
#
# short-lived opaque token minted in Slack, redeemed once the user comes back
# from keycloak with a matching (iss, sub).


class SlackLinkTokenBase(Base):
    # opaque secrets.token_urlsafe(32); unguessable, single use
    token: str = Field(index=True, unique=True)
    slack_team_id: str = Field(index=True)
    slack_user_id: str = Field(index=True)
    expires_at: datetime.datetime


class SlackLinkToken(SlackLinkTokenBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)

    consumed: bool = Field(
        default=False,
        sa_column=Column(
            Boolean,
            nullable=False,
            server_default=sa.false(),
        ),
    )
    consumed_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )
