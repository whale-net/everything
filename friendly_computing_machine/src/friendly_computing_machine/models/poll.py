"""Ad-hoc `/poll` models.

Slack identities are stored as Slack IDs rather than FKs to `slackuser`/`slackchannel`
because those tables are filled by a periodic sync and may not yet contain a new voter.
"""

import datetime

from sqlalchemy import Column, DateTime, Index, UniqueConstraint, func, text
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class PollBase(Base):
    question: str
    slack_channel_slack_id: str = Field(index=True)
    creator_slack_user_slack_id: str = Field(index=True)
    # set once the poll message has been posted
    slack_message_ts: str | None = Field(default=None, nullable=True)
    anonymous: bool = Field(default=False)
    # max active votes per person; NULL = unlimited
    vote_limit: int | None = Field(default=None, nullable=True)


class Poll(PollBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )
    closed_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class PollOptionBase(Base):
    poll_id: int = Field(foreign_key="poll.id", index=True)
    # 0-based display order
    position: int
    text: str


class PollOption(PollOptionBase, table=True):
    __table_args__ = (UniqueConstraint("poll_id", "position"),)
    id: int = Field(default=None, nullable=False, primary_key=True)


class PollVote(Base, table=True):
    """One row per vote cast; un-voting stamps `removed_at` so the full history is kept."""

    __table_args__ = (
        # a person holds at most one active vote per option
        Index(
            "uq_pollvote_active",
            "poll_option_id",
            "slack_user_slack_id",
            unique=True,
            postgresql_where=text("removed_at IS NULL"),
            sqlite_where=text("removed_at IS NULL"),
        ),
    )

    id: int = Field(default=None, nullable=False, primary_key=True)
    poll_id: int = Field(foreign_key="poll.id", index=True)
    poll_option_id: int = Field(foreign_key="polloption.id", index=True)
    slack_user_slack_id: str = Field(index=True)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )
    removed_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
