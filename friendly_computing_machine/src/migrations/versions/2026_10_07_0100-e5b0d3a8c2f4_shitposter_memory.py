"""shitposter_memory

Persona attributes and lore (SCD2) plus the append-only memory change log.
Every change row carries a cause; the change log is not SCD2.

Revision ID: e5b0d3a8c2f4
Revises: d4a9c1e7b3f2
Create Date: 2026-10-07 01:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "e5b0d3a8c2f4"
down_revision: Union[str, None] = "d4a9c1e7b3f2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def _scd2_columns() -> list:
    return [
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("valid_to", sa.DateTime(timezone=True), nullable=True),
    ]


def upgrade() -> None:
    op.create_table(
        "shitposterpersonaattribute",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column(
            "attribute_key", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("status", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("retired_by_operator", sa.Boolean(), nullable=False),
        *_scd2_columns(),
        sa.CheckConstraint(
            "status IN ('active', 'retired', 'merged')",
            name="ck_shitposterpersonaattribute_status",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterpersonaattribute_persona_id"),
        "shitposterpersonaattribute",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitposterpersonaattribute_current",
        "shitposterpersonaattribute",
        ["persona_id", "attribute_key"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
        sqlite_where=sa.text("valid_to IS NULL"),
    )

    op.create_table(
        "shitposterloreentry",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("source_post_id", sa.Integer(), nullable=True),
        sa.Column("popularity_score", sa.Integer(), nullable=False),
        sa.Column("retired_by_operator", sa.Boolean(), nullable=False),
        *_scd2_columns(),
        sa.CheckConstraint(
            "kind IN ('hit', 'consolidated')",
            name="ck_shitposterloreentry_kind",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(["source_post_id"], ["fcm.shitposterpost.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterloreentry_persona_id"),
        "shitposterloreentry",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "ix_shitposterloreentry_current",
        "shitposterloreentry",
        ["persona_id"],
        unique=False,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
        sqlite_where=sa.text("valid_to IS NULL"),
    )

    op.create_table(
        "shitpostermemorychange",
        sa.Column(
            "changed_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column(
            "entity_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("entity_id", sa.Integer(), nullable=True),
        sa.Column(
            "operation", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "before_text", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column(
            "after_text", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column(
            "cause_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("cause_post_id", sa.Integer(), nullable=True),
        sa.Column("cause_ref", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column(
            "reflector_run_id", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "entity_kind IN ('attribute', 'lore')",
            name="ck_shitpostermemorychange_entity_kind",
        ),
        sa.CheckConstraint(
            "operation IN ('add', 'reinforce', 'retire', 'merge', 'fold', 'promote')",
            name="ck_shitpostermemorychange_operation",
        ),
        sa.CheckConstraint(
            "cause_kind IN ('post_engagement', 'suggestion', 'operator', 'fold')",
            name="ck_shitpostermemorychange_cause_kind",
        ),
        # every change names at least one cause
        sa.CheckConstraint(
            "cause_post_id IS NOT NULL OR cause_ref IS NOT NULL",
            name="ck_shitpostermemorychange_has_cause",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(["cause_post_id"], ["fcm.shitposterpost.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostermemorychange_persona_id"),
        "shitpostermemorychange",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_shitpostermemorychange_persona_id"),
        table_name="shitpostermemorychange",
        schema="fcm",
    )
    op.drop_table("shitpostermemorychange", schema="fcm")
    op.drop_index(
        "ix_shitposterloreentry_current",
        table_name="shitposterloreentry",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterloreentry_persona_id"),
        table_name="shitposterloreentry",
        schema="fcm",
    )
    op.drop_table("shitposterloreentry", schema="fcm")
    op.drop_index(
        "uq_shitposterpersonaattribute_current",
        table_name="shitposterpersonaattribute",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterpersonaattribute_persona_id"),
        table_name="shitposterpersonaattribute",
        schema="fcm",
    )
    op.drop_table("shitposterpersonaattribute", schema="fcm")
