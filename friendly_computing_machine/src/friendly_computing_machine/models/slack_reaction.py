"""Captured Slack reactions, one row per (message, user, emoji) add.

Identity is (channel, message ts) plus a nullable FK so a reaction on a
not-yet-stored message is kept. Removal stamps `removed_at`; re-adding opens a
new row. Emoji are stored as the Slack reaction name with any
`::skin-tone-N` suffix stripped, so tone variants count as one emoji.
"""

import datetime

from sqlalchemy import Column, DateTime, Index, text
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class SlackReaction(Base, table=True):
    __table_args__ = (
        # at most one active row per (message, user, emoji)
        Index(
            "uq_slackreaction_active",
            "slack_channel_slack_id",
            "message_ts",
            "slack_user_slack_id",
            "emoji",
            unique=True,
            postgresql_where=text("removed_at IS NULL"),
            sqlite_where=text("removed_at IS NULL"),
        ),
    )

    id: int = Field(default=None, nullable=False, primary_key=True)
    slack_message_id: int | None = Field(
        default=None, nullable=True, foreign_key="slackmessage.id", index=True
    )
    slack_channel_slack_id: str = Field(index=True)
    # Slack ts string of the reacted-to message
    message_ts: str
    slack_user_slack_id: str
    emoji: str
    # null when the time is unknown (backfill)
    added_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    removed_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    added_by_backfill: bool = Field(default=False)
    removed_by_backfill: bool = Field(default=False)
    is_bot: bool | None = Field(default=None, nullable=True)
