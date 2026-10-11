"""shitposter_source_weight

Per-persona SCD2 target weights for the theme and novel lore sources; each
revision records its cause.

Revision ID: a4c7e2f9b1d3
Revises: b7039c37db2c
Create Date: 2026-10-11 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "a4c7e2f9b1d3"
down_revision: Union[str, Sequence[str], None] = "b7039c37db2c"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

TABLE = "shitpostersourceweight"


def upgrade() -> None:
    op.create_table(
        TABLE,
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("source", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("weight", sa.Numeric(6, 4), nullable=False),
        sa.Column(
            "cause",
            sa.JSON().with_variant(postgresql.JSONB(), "postgresql"),
            nullable=False,
        ),
        sa.Column("brain_job_run_id", sa.Integer(), nullable=True),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("valid_to", sa.DateTime(timezone=True), nullable=True),
        sa.CheckConstraint(
            "source IN ('theme', 'novel')",
            name="ck_shitpostersourceweight_source",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(
            ["brain_job_run_id"], ["fcm.shitposterbrainjobrun.id"]
        ),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersourceweight_persona_id"),
        TABLE,
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitpostersourceweight_current",
        TABLE,
        ["persona_id", "source"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
        sqlite_where=sa.text("valid_to IS NULL"),
    )


def downgrade() -> None:
    op.drop_index("uq_shitpostersourceweight_current", table_name=TABLE, schema="fcm")
    op.drop_index(
        op.f("ix_fcm_shitpostersourceweight_persona_id"),
        table_name=TABLE,
        schema="fcm",
    )
    op.drop_table(TABLE, schema="fcm")
