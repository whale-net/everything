import datetime
from enum import Enum
from typing import Any, Dict, Optional

from sqlmodel import Field, Relationship

from friendly_computing_machine.models.base import Base
from friendly_computing_machine.util import ts_to_datetime


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
