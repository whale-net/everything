"""shitposter_post_context_snapshot

Records the context snapshot each bot post was written from, and adds a
skip record for scheduled slots that produced no post because the writer
was unavailable.

Revision ID: b3d8f1e6a2c9
Revises: c8e2f5a1d9b3
Create Date: 2026-10-07 09:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "b3d8f1e6a2c9"
down_revision: Union[str, Sequence[str], None] = "c8e2f5a1d9b3"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.add_column(
        "shitposterpost",
        sa.Column("context_snapshot_id", sa.Integer(), nullable=True),
        schema="fcm",
    )
    op.create_foreign_key(
        "shitposterpost_context_snapshot_id_fkey",
        "shitposterpost",
        "shitpostercontextsnapshot",
        ["context_snapshot_id"],
        ["id"],
        source_schema="fcm",
        referent_schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterpost_context_snapshot_id"),
        "shitposterpost",
        ["context_snapshot_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "shitposterscheduledskip",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.Column("reason", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("context_snapshot_id", sa.Integer(), nullable=True),
        sa.Column(
            "skipped_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.CheckConstraint(
            "reason IN ('writer_unavailable')",
            name="ck_shitposterscheduledskip_reason",
        ),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(["slack_channel_id"], ["fcm.slackchannel.id"]),
        sa.ForeignKeyConstraint(
            ["context_snapshot_id"], ["fcm.shitpostercontextsnapshot.id"]
        ),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterscheduledskip_persona_id"),
        "shitposterscheduledskip",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterscheduledskip_slack_channel_id"),
        "shitposterscheduledskip",
        ["slack_channel_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_shitposterscheduledskip_slack_channel_id"),
        table_name="shitposterscheduledskip",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterscheduledskip_persona_id"),
        table_name="shitposterscheduledskip",
        schema="fcm",
    )
    op.drop_table("shitposterscheduledskip", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_shitposterpost_context_snapshot_id"),
        table_name="shitposterpost",
        schema="fcm",
    )
    op.drop_constraint(
        "shitposterpost_context_snapshot_id_fkey",
        "shitposterpost",
        type_="foreignkey",
        schema="fcm",
    )
    op.drop_column("shitposterpost", "context_snapshot_id", schema="fcm")
