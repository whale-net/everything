"""slack_reactions

Revision ID: e5b8d3f1a6c4
Revises: d4a9c1e7b3f2
Create Date: 2026-10-07 01:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "e5b8d3f1a6c4"
down_revision = "d4a9c1e7b3f2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "slackreaction",
        sa.Column("slack_message_id", sa.Integer(), nullable=True),
        sa.Column(
            "slack_channel_slack_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column(
            "message_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "slack_user_slack_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column("emoji", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("added_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("removed_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("added_by_backfill", sa.Boolean(), nullable=False),
        sa.Column("removed_by_backfill", sa.Boolean(), nullable=False),
        sa.Column("is_bot", sa.Boolean(), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.ForeignKeyConstraint(["slack_message_id"], ["fcm.slackmessage.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackreaction_slack_message_id"),
        "slackreaction",
        ["slack_message_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackreaction_slack_channel_slack_id"),
        "slackreaction",
        ["slack_channel_slack_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_slackreaction_active",
        "slackreaction",
        ["slack_channel_slack_id", "message_ts", "slack_user_slack_id", "emoji"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("removed_at IS NULL"),
    )


def downgrade() -> None:
    op.drop_index(
        "uq_slackreaction_active", table_name="slackreaction", schema="fcm"
    )
    op.drop_index(
        op.f("ix_fcm_slackreaction_slack_channel_slack_id"),
        table_name="slackreaction",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_slackreaction_slack_message_id"),
        table_name="slackreaction",
        schema="fcm",
    )
    op.drop_table("slackreaction", schema="fcm")
