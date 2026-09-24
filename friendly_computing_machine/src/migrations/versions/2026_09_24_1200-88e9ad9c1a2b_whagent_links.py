"""whagent_links

Revision ID: 88e9ad9c1a2b
Revises: b7c3d91e4f20
Create Date: 2026-09-24 12:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "88e9ad9c1a2b"
down_revision: Union[str, None] = "b7c3d91e4f20"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "slackchannelagentlink",
        sa.Column(
            "whagent_agent_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.ForeignKeyConstraint(
            ["slack_channel_id"],
            ["fcm.slackchannel.id"],
        ),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackchannelagentlink_slack_channel_id"),
        "slackchannelagentlink",
        ["slack_channel_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "slackthreadsession",
        sa.Column("thread_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "whagent_session_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "status",
            sa.Enum("ACTIVE", "CLOSED", name="slackthreadsessionstatus"),
            nullable=False,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.ForeignKeyConstraint(
            ["slack_channel_id"],
            ["fcm.slackchannel.id"],
        ),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint(
            "slack_channel_id", "thread_ts", name="uq_slackthreadsession_channel_thread"
        ),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackthreadsession_slack_channel_id"),
        "slackthreadsession",
        ["slack_channel_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackthreadsession_thread_ts"),
        "slackthreadsession",
        ["thread_ts"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_slackthreadsession_thread_ts"),
        table_name="slackthreadsession",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_slackthreadsession_slack_channel_id"),
        table_name="slackthreadsession",
        schema="fcm",
    )
    op.drop_table("slackthreadsession", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_slackchannelagentlink_slack_channel_id"),
        table_name="slackchannelagentlink",
        schema="fcm",
    )
    op.drop_table("slackchannelagentlink", schema="fcm")
