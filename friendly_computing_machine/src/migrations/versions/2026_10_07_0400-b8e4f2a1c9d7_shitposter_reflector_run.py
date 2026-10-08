"""shitposter_reflector_run

Revision ID: b8e4f2a1c9d7
Revises: 2a73c1430e44
Create Date: 2026-10-07 04:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "b8e4f2a1c9d7"
down_revision: Union[str, None] = "2a73c1430e44"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitposterreflectorrun",
        sa.Column("brain_job_run_id", sa.Integer(), nullable=False),
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("input_count", sa.Integer(), nullable=False),
        sa.Column("carried_over_count", sa.Integer(), nullable=False),
        sa.Column("applied_count", sa.Integer(), nullable=False),
        sa.Column("rejection_count", sa.Integer(), nullable=False),
        sa.Column(
            "rejections",
            sa.JSON().with_variant(postgresql.JSONB(), "postgresql"),
            server_default=sa.text("'[]'"),
            nullable=False,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "input_count >= 0 AND carried_over_count >= 0 "
            "AND applied_count >= 0 AND rejection_count >= 0",
            name="ck_shitposterreflectorrun_counts_nonnegative",
        ),
        sa.ForeignKeyConstraint(
            ["brain_job_run_id"], ["fcm.shitposterbrainjobrun.id"]
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("brain_job_run_id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterreflectorrun_persona_id"),
        "shitposterreflectorrun",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_shitposterreflectorrun_persona_id"),
        table_name="shitposterreflectorrun",
        schema="fcm",
    )
    op.drop_table("shitposterreflectorrun", schema="fcm")
