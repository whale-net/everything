"""Scheduled music-poll run history.

One row per poll run per channel, with the options that run posted,
so a run's picks stay queryable after its poll closes and later
features can rank songs across past weekly polls.
"""

import datetime

from sqlalchemy import Column, DateTime, UniqueConstraint, func
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class ScheduledPollRunBase(Base):
    # the run's identity: the scheduled fire time (ISO 8601) for
    # scheduled runs, or the Temporal workflow run id for manual runs
    run_identity: str = Field(index=True)
    slack_channel_slack_id: str = Field(index=True)
    # when the run executed
    run_at: datetime.datetime = Field(
        sa_column=Column(DateTime(timezone=True), nullable=False)
    )
    # scheduled runs: the fire time the run was scheduled for
    scheduled_fire_time: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    # manual runs: the Temporal workflow run id
    workflow_run_id: str | None = Field(default=None, nullable=True)
    # the poll this run posted; set once the poll has been created
    poll_id: int | None = Field(
        default=None, nullable=True, foreign_key="poll.id", index=True
    )
    # written after the Slack post succeeds
    slack_message_ts: str | None = Field(default=None, nullable=True)


class ScheduledPollRun(ScheduledPollRunBase, table=True):
    __table_args__ = (
        # one row per run identity per channel, so a retried or
        # replayed run never records a second poll
        UniqueConstraint("run_identity", "slack_channel_slack_id"),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True),
            nullable=False,
            server_default=func.current_timestamp(),
        ),
    )


class ScheduledPollRunOptionBase(Base):
    scheduled_poll_run_id: int = Field(
        foreign_key="scheduledpollrun.id", index=True
    )
    # the poll option this run posted, so per-option vote counts stay
    # queryable after the poll closes
    poll_option_id: int | None = Field(
        default=None, nullable=True, foreign_key="polloption.id", index=True
    )
    # 0-based display order, matching the posted poll's option order
    position: int
    # normalized song identity: the Spotify track ID or YouTube video
    # ID when present, otherwise the URL with tracking parameters stripped
    song_identity: str
    song_link: str
    submitter_slack_user_slack_id: str = Field(index=True)
    # when the submitter's Slack message was originally posted, as UTC
    submission_date: datetime.datetime = Field(
        sa_column=Column(DateTime(timezone=True), nullable=False)
    )


class ScheduledPollRunOption(ScheduledPollRunOptionBase, table=True):
    __table_args__ = (
        UniqueConstraint("scheduled_poll_run_id", "position"),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
