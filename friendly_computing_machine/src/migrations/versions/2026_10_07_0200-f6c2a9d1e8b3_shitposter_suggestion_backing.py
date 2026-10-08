"""shitposter_suggestion_backing

Revision ID: f6c2a9d1e8b3
Revises: e5b1f8a2c9d4
Create Date: 2026-10-07 02:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "f6c2a9d1e8b3"
down_revision: Union[str, None] = "e5b1f8a2c9d4"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitpostersuggestionbacker",
        sa.Column("suggestion_id", sa.Integer(), nullable=False),
        sa.Column(
            "slack_user_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "backed_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("removed_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.ForeignKeyConstraint(["suggestion_id"], ["fcm.shitpostersuggestion.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersuggestionbacker_suggestion_id"),
        "shitpostersuggestionbacker",
        ["suggestion_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitpostersuggestionbacker_active",
        "shitpostersuggestionbacker",
        ["suggestion_id", "slack_user_id"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("removed_at IS NULL"),
    )

    op.create_table(
        "shitpostersuggestionreplyoutbox",
        sa.Column("suggestion_id", sa.Integer(), nullable=False),
        sa.Column("kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "coarse_reason", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("posted_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "kind IN ('applied', 'declined', 'expired')",
            name="ck_shitpostersuggestionreplyoutbox_kind",
        ),
        sa.CheckConstraint(
            "coarse_reason IS NULL OR coarse_reason IN "
            "('off_topic', 'unsafe', 'duplicate', 'other')",
            name="ck_shitpostersuggestionreplyoutbox_coarse_reason",
        ),
        sa.ForeignKeyConstraint(["suggestion_id"], ["fcm.shitpostersuggestion.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersuggestionreplyoutbox_suggestion_id"),
        "shitpostersuggestionreplyoutbox",
        ["suggestion_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "ix_shitpostersuggestionreplyoutbox_unposted",
        "shitpostersuggestionreplyoutbox",
        ["created_at"],
        unique=False,
        schema="fcm",
        postgresql_where=sa.text("posted_at IS NULL"),
    )


def downgrade() -> None:
    op.drop_index(
        "ix_shitpostersuggestionreplyoutbox_unposted",
        table_name="shitpostersuggestionreplyoutbox",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitpostersuggestionreplyoutbox_suggestion_id"),
        table_name="shitpostersuggestionreplyoutbox",
        schema="fcm",
    )
    op.drop_table("shitpostersuggestionreplyoutbox", schema="fcm")
    op.drop_index(
        "uq_shitpostersuggestionbacker_active",
        table_name="shitpostersuggestionbacker",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitpostersuggestionbacker_suggestion_id"),
        table_name="shitpostersuggestionbacker",
        schema="fcm",
    )
    op.drop_table("shitpostersuggestionbacker", schema="fcm")
