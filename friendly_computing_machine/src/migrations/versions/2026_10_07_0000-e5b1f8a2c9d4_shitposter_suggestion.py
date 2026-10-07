"""shitposter_suggestion

Revision ID: e5b1f8a2c9d4
Revises: d4a9c1e7b3f2
Create Date: 2026-10-07 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "e5b1f8a2c9d4"
down_revision: Union[str, None] = "d4a9c1e7b3f2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitpostersuggestion",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.Column(
            "slack_message_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "submitter_slack_user_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column("text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "status",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
            server_default="pending",
        ),
        sa.Column(
            "submitted_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column(
            "status_changed_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "status IN ('pending', 'promoted', 'applied', 'declined', 'expired')",
            name="ck_shitpostersuggestion_status",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(["slack_channel_id"], ["fcm.slackchannel.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint(
            "slack_channel_id",
            "slack_message_ts",
            name="uq_shitpostersuggestion_channel_ts",
        ),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersuggestion_persona_id"),
        "shitpostersuggestion",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersuggestion_slack_channel_id"),
        "shitpostersuggestion",
        ["slack_channel_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "ix_shitpostersuggestion_status_expires_at",
        "shitpostersuggestion",
        ["status", "expires_at"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        "ix_shitpostersuggestion_status_expires_at",
        table_name="shitpostersuggestion",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitpostersuggestion_slack_channel_id"),
        table_name="shitpostersuggestion",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitpostersuggestion_persona_id"),
        table_name="shitpostersuggestion",
        schema="fcm",
    )
    op.drop_table("shitpostersuggestion", schema="fcm")
