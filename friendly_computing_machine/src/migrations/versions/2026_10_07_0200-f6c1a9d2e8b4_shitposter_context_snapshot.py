"""shitposter_context_snapshot

Versioned context snapshots per persona and the items each snapshot ranked in.
Also merges the two sibling heads (brain job run, memory) into one chain head.
Snapshots are append-only; item rows point at the SCD2 row version they used.

Revision ID: f6c1a9d2e8b4
Revises: e5b0d3a8c2f4, e5b0d3a8c2f5
Create Date: 2026-10-07 02:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "f6c1a9d2e8b4"
down_revision: Union[str, Sequence[str], None] = ("e5b0d3a8c2f4", "e5b0d3a8c2f5")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitpostercontextsnapshot",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("version", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("brain_job_run_id", sa.Integer(), nullable=False),
        sa.Column("token_count", sa.Integer(), nullable=False),
        sa.Column("rendered_text", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint("version >= 1", name="ck_shitpostercontextsnapshot_version"),
        sa.CheckConstraint(
            "token_count >= 0", name="ck_shitpostercontextsnapshot_token_count"
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(["brain_job_run_id"], ["fcm.shitposterbrainjobrun.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint(
            "persona_id", "version", name="uq_shitpostercontextsnapshot_persona_version"
        ),
        schema="fcm",
    )

    op.create_table(
        "shitpostersnapshotitem",
        sa.Column("snapshot_id", sa.Integer(), nullable=False),
        sa.Column("item_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        # id of the SCD2 attribute or lore row version included in the snapshot
        sa.Column("item_id", sa.Integer(), nullable=False),
        sa.Column("rank", sa.Integer(), nullable=False),
        sa.Column("is_random_pick", sa.Boolean(), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "item_kind IN ('attribute', 'lore')",
            name="ck_shitpostersnapshotitem_item_kind",
        ),
        sa.ForeignKeyConstraint(["snapshot_id"], ["fcm.shitpostercontextsnapshot.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitpostersnapshotitem_snapshot_id"),
        "shitpostersnapshotitem",
        ["snapshot_id"],
        unique=False,
        schema="fcm",
    )
    # "last appeared in snapshot" lookups by item
    op.create_index(
        "ix_shitpostersnapshotitem_item",
        "shitpostersnapshotitem",
        ["item_kind", "item_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        "ix_shitpostersnapshotitem_item",
        table_name="shitpostersnapshotitem",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitpostersnapshotitem_snapshot_id"),
        table_name="shitpostersnapshotitem",
        schema="fcm",
    )
    op.drop_table("shitpostersnapshotitem", schema="fcm")
    op.drop_table("shitpostercontextsnapshot", schema="fcm")
