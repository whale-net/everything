"""slack_keycloak_identity

Revision ID: c1d4e8a7b2f9
Revises: 88e9ad9c1a2b
Create Date: 2026-09-24 14:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "c1d4e8a7b2f9"
down_revision: Union[str, None] = "88e9ad9c1a2b"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.create_table(
        "slackkeycloakidentity",
        sa.Column(
            "slack_team_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "slack_user_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("keycloak_iss", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("keycloak_sub", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint(
            "slack_team_id", "slack_user_id", name="uq_slackkeycloakidentity_slack_user"
        ),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackkeycloakidentity_slack_user_id"),
        "slackkeycloakidentity",
        ["slack_user_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slackkeycloakidentity_slack_team_id"),
        "slackkeycloakidentity",
        ["slack_team_id"],
        unique=False,
        schema="fcm",
    )

    op.create_table(
        "slacklinktoken",
        sa.Column("token", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "slack_team_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "slack_user_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column(
            "consumed",
            sa.Boolean(),
            server_default=sa.false(),
            nullable=False,
        ),
        sa.Column("consumed_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slacklinktoken_token"),
        "slacklinktoken",
        ["token"],
        unique=True,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slacklinktoken_slack_user_id"),
        "slacklinktoken",
        ["slack_user_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_slacklinktoken_slack_team_id"),
        "slacklinktoken",
        ["slack_team_id"],
        unique=False,
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_index(
        op.f("ix_fcm_slacklinktoken_slack_team_id"),
        table_name="slacklinktoken",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_slacklinktoken_slack_user_id"),
        table_name="slacklinktoken",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_slacklinktoken_token"),
        table_name="slacklinktoken",
        schema="fcm",
    )
    op.drop_table("slacklinktoken", schema="fcm")
    op.drop_index(
        op.f("ix_fcm_slackkeycloakidentity_slack_team_id"),
        table_name="slackkeycloakidentity",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_slackkeycloakidentity_slack_user_id"),
        table_name="slackkeycloakidentity",
        schema="fcm",
    )
    op.drop_table("slackkeycloakidentity", schema="fcm")
