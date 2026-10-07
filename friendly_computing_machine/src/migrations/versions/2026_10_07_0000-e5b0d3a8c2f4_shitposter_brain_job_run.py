"""shitposter_brain_job_run

Revision ID: e5b0d3a8c2f4
Revises: d4a9c1e7b3f2
Create Date: 2026-10-07 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "e5b0d3a8c2f4"
down_revision: Union[str, None] = "d4a9c1e7b3f2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitposterbrainjobrun",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("job_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("trigger", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("status", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "started_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("finished_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("skip_reason", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column("error", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column(
            "details",
            sa.JSON().with_variant(postgresql.JSONB(), "postgresql"),
            nullable=True,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "job_kind IN ('harvest', 'reflect', 'write', 'snapshot')",
            name="ck_shitposterbrainjobrun_job_kind",
        ),
        sa.CheckConstraint(
            "trigger IN ('schedule', 'operator')",
            name="ck_shitposterbrainjobrun_trigger",
        ),
        sa.CheckConstraint(
            "status IN ('running', 'succeeded', 'failed', 'skipped', 'no_op')",
            name="ck_shitposterbrainjobrun_status",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterbrainjobrun_persona_id"),
        "shitposterbrainjobrun",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitposterbrainjobrun_running",
        "shitposterbrainjobrun",
        ["persona_id"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("status = 'running'"),
        sqlite_where=sa.text("status = 'running'"),
    )


def downgrade() -> None:
    op.drop_index(
        "uq_shitposterbrainjobrun_running",
        table_name="shitposterbrainjobrun",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterbrainjobrun_persona_id"),
        table_name="shitposterbrainjobrun",
        schema="fcm",
    )
    op.drop_table("shitposterbrainjobrun", schema="fcm")
