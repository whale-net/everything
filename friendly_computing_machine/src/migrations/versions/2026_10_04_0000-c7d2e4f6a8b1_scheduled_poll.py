"""scheduled_poll

Revision ID: c7d2e4f6a8b1
Revises: a3e7d51c9b42
Create Date: 2026-10-04 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "c7d2e4f6a8b1"
down_revision: Union[str, None] = "a3e7d51c9b42"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # a scheduled poll is posted by the weekly music-poll
    # schedule rather than a person via /wpoll
    op.add_column(
        "poll",
        sa.Column(
            "automated",
            sa.Boolean(),
            nullable=False,
            server_default=sa.false(),
        ),
        schema="fcm",
    )

    op.create_table(
        "scheduledpollrun",
        sa.Column(
            "run_identity", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "slack_channel_slack_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column("run_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column(
            "scheduled_fire_time", sa.DateTime(timezone=True), nullable=True
        ),
        sa.Column(
            "workflow_run_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column("poll_id", sa.Integer(), nullable=True),
        sa.Column(
            "slack_message_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.ForeignKeyConstraint(["poll_id"], ["fcm.poll.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("run_identity", "slack_channel_slack_id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrun_slack_channel_slack_id"),
        "scheduledpollrun",
        ["slack_channel_slack_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrun_run_identity"),
        "scheduledpollrun",
        ["run_identity"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrun_poll_id"),
        "scheduledpollrun",
        ["poll_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "scheduledpollrunoption",
        sa.Column(
            "scheduled_poll_run_id", sa.Integer(), nullable=False
        ),
        sa.Column("poll_option_id", sa.Integer(), nullable=True),
        sa.Column("position", sa.Integer(), nullable=False),
        sa.Column(
            "song_identity", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "song_link", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "submitter_slack_user_slack_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column("submission_date", sa.DateTime(timezone=True), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.ForeignKeyConstraint(
            ["scheduled_poll_run_id"], ["fcm.scheduledpollrun.id"]
        ),
        sa.ForeignKeyConstraint(["poll_option_id"], ["fcm.polloption.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("scheduled_poll_run_id", "position"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrunoption_scheduled_poll_run_id"),
        "scheduledpollrunoption",
        ["scheduled_poll_run_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrunoption_poll_option_id"),
        "scheduledpollrunoption",
        ["poll_option_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_scheduledpollrunoption_submitter_slack_user_slack_id"),
        "scheduledpollrunoption",
        ["submitter_slack_user_slack_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_scheduledpollrunoption_submitter_slack_user_slack_id"),
        table_name="scheduledpollrunoption",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_scheduledpollrunoption_poll_option_id"),
        table_name="scheduledpollrunoption",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_scheduledpollrunoption_scheduled_poll_run_id"),
        table_name="scheduledpollrunoption",
        schema="fcm",
    )
    op.drop_table("scheduledpollrunoption", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_scheduledpollrun_poll_id"),
        table_name="scheduledpollrun",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_scheduledpollrun_slack_channel_slack_id"),
        table_name="scheduledpollrun",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_scheduledpollrun_run_identity"),
        table_name="scheduledpollrun",
        schema="fcm",
    )
    op.drop_table("scheduledpollrun", schema="fcm")
    op.drop_column("poll", "automated", schema="fcm")
