"""shitposter_post_engagement

Revision ID: f6c1a9d4e2b7
Revises: e5b0d3a8c2f4
Create Date: 2026-10-07 02:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "f6c1a9d4e2b7"
down_revision: Union[str, None] = "e5b0d3a8c2f4"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitposterpostengagement",
        sa.Column("post_id", sa.Integer(), nullable=False),
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column(
            "finalized_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("distinct_reactors", sa.Integer(), nullable=False),
        sa.Column(
            "reactions_by_emoji",
            sa.JSON().with_variant(postgresql.JSONB(), "postgresql"),
            nullable=False,
        ),
        sa.Column("distinct_repliers", sa.Integer(), nullable=False),
        sa.Column("negative_reactions", sa.Integer(), nullable=False),
        sa.Column("consumed_by_reflector_run_id", sa.Integer(), nullable=True),
        sa.Column("consumed_by_lore_run_id", sa.Integer(), nullable=True),
        sa.ForeignKeyConstraint(["post_id"], ["fcm.shitposterpost.id"]),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(
            ["consumed_by_reflector_run_id"], ["fcm.shitposterbrainjobrun.id"]
        ),
        sa.ForeignKeyConstraint(
            ["consumed_by_lore_run_id"], ["fcm.shitposterbrainjobrun.id"]
        ),
        sa.PrimaryKeyConstraint("post_id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterpostengagement_persona_id"),
        "shitposterpostengagement",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_shitposterpostengagement_persona_id"),
        table_name="shitposterpostengagement",
        schema="fcm",
    )
    op.drop_table("shitposterpostengagement", schema="fcm")
