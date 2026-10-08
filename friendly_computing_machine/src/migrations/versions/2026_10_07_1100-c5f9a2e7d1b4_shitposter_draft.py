"""shitposter_draft

Adds the writer's draft queue: drafts written ahead of scheduled slots, each
tied to the context snapshot and write job run that produced it.

Revision ID: c5f9a2e7d1b4
Revises: e4a7c0d2b913
Create Date: 2026-10-07 11:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "c5f9a2e7d1b4"
down_revision: Union[str, Sequence[str], None] = "e4a7c0d2b913"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitposterdraft",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("snapshot_id", sa.Integer(), nullable=False),
        sa.Column("brain_job_run_id", sa.Integer(), nullable=False),
        sa.Column("text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("rank", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("used_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("used_post_id", sa.Integer(), nullable=True),
        sa.Column("discarded_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("discard_reason", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint("rank >= 1", name="ck_shitposterdraft_rank"),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(
            ["snapshot_id"], ["fcm.shitpostercontextsnapshot.id"]
        ),
        sa.ForeignKeyConstraint(
            ["brain_job_run_id"], ["fcm.shitposterbrainjobrun.id"]
        ),
        sa.ForeignKeyConstraint(["used_post_id"], ["fcm.shitposterpost.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint("used_post_id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterdraft_persona_id"),
        "shitposterdraft",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterdraft_snapshot_id"),
        "shitposterdraft",
        ["snapshot_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "ix_shitposterdraft_unused",
        "shitposterdraft",
        ["persona_id", "rank"],
        unique=False,
        schema="fcm",
        postgresql_where=sa.text("used_at IS NULL AND discarded_at IS NULL"),
    )


def downgrade() -> None:
    op.drop_index(
        "ix_shitposterdraft_unused",
        table_name="shitposterdraft",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterdraft_snapshot_id"),
        table_name="shitposterdraft",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterdraft_persona_id"),
        table_name="shitposterdraft",
        schema="fcm",
    )
    op.drop_table("shitposterdraft", schema="fcm")
