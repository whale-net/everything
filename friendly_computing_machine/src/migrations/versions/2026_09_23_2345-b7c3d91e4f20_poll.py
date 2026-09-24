"""poll

Revision ID: b7c3d91e4f20
Revises: 71e2c8de4b19
Create Date: 2026-09-23 23:45:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "b7c3d91e4f20"
down_revision: Union[str, None] = "71e2c8de4b19"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "poll",
        sa.Column("question", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "slack_channel_slack_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "creator_slack_user_slack_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column(
            "slack_message_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column("anonymous", sa.Boolean(), nullable=False),
        sa.Column("vote_limit", sa.Integer(), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.Column("closed_at", sa.DateTime(timezone=True), nullable=True),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_poll_slack_channel_slack_id"),
        "poll",
        ["slack_channel_slack_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_poll_creator_slack_user_slack_id"),
        "poll",
        ["creator_slack_user_slack_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "polloption",
        sa.Column("poll_id", sa.Integer(), nullable=False),
        sa.Column("position", sa.Integer(), nullable=False),
        sa.Column("text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.ForeignKeyConstraint(["poll_id"], ["fcm.poll.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("poll_id", "position"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_polloption_poll_id"),
        "polloption",
        ["poll_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "pollvote",
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("poll_id", sa.Integer(), nullable=False),
        sa.Column("poll_option_id", sa.Integer(), nullable=False),
        sa.Column(
            "slack_user_slack_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.Column("removed_at", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(["poll_id"], ["fcm.poll.id"]),
        sa.ForeignKeyConstraint(["poll_option_id"], ["fcm.polloption.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_pollvote_poll_id"),
        "pollvote",
        ["poll_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_pollvote_poll_option_id"),
        "pollvote",
        ["poll_option_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_pollvote_slack_user_slack_id"),
        "pollvote",
        ["slack_user_slack_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_pollvote_active",
        "pollvote",
        ["poll_option_id", "slack_user_slack_id"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("removed_at IS NULL"),
    )


def downgrade() -> None:
    op.drop_index("uq_pollvote_active", table_name="pollvote", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_pollvote_slack_user_slack_id"), table_name="pollvote", schema="fcm"
    )
    op.drop_index(
        op.f("ix_fcm_pollvote_poll_option_id"), table_name="pollvote", schema="fcm"
    )
    op.drop_index(op.f("ix_fcm_pollvote_poll_id"), table_name="pollvote", schema="fcm")
    op.drop_table("pollvote", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_polloption_poll_id"), table_name="polloption", schema="fcm"
    )
    op.drop_table("polloption", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_poll_creator_slack_user_slack_id"), table_name="poll", schema="fcm"
    )
    op.drop_index(
        op.f("ix_fcm_poll_slack_channel_slack_id"), table_name="poll", schema="fcm"
    )
    op.drop_table("poll", schema="fcm")
