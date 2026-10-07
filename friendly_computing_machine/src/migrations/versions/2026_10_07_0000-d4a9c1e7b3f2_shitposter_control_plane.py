"""shitposter_control_plane

Revision ID: d4a9c1e7b3f2
Revises: c7d2e4f6a8b1
Create Date: 2026-10-07 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "d4a9c1e7b3f2"
down_revision = "d4a9b2c7e1f3"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "shitposterchanneloptin",
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.Column("opted_in", sa.Boolean(), nullable=False),
        sa.Column(
            "set_by_slack_user_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=False,
        ),
        sa.Column(
            "set_by_slack_team_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=True,
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("valid_to", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(["slack_channel_id"], ["fcm.slackchannel.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterchanneloptin_slack_channel_id"),
        "shitposterchanneloptin",
        ["slack_channel_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitposterchanneloptin_current",
        "shitposterchanneloptin",
        ["slack_channel_id"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
    )

    # no current row means Shitposter is ON; no seed row is written
    op.create_table(
        "shitposterkillswitch",
        sa.Column(
            "scope",
            sqlmodel.sql.sqltypes.AutoString(),
            server_default="workspace",
            nullable=False,
        ),
        sa.Column("enabled", sa.Boolean(), nullable=False),
        sa.Column(
            "set_by", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("reason", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("valid_to", sa.DateTime(timezone=True), nullable=True),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        "uq_shitposterkillswitch_current",
        "shitposterkillswitch",
        ["scope"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
    )


def downgrade() -> None:
    op.drop_index(
        "uq_shitposterkillswitch_current",
        table_name="shitposterkillswitch",
        schema="fcm",
    )
    op.drop_table("shitposterkillswitch", schema="fcm")
    op.drop_index(
        "uq_shitposterchanneloptin_current",
        table_name="shitposterchanneloptin",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterchanneloptin_slack_channel_id"),
        table_name="shitposterchanneloptin",
        schema="fcm",
    )
    op.drop_table("shitposterchanneloptin", schema="fcm")
