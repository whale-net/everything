"""shitposter_persona_posts

Revision ID: d4a9b2c7e1f3
Revises: c7d2e4f6a8b1
Create Date: 2026-10-07 00:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "d4a9b2c7e1f3"
down_revision: Union[str, None] = "c7d2e4f6a8b1"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

SEED_PERSONA_NAME = "shitposter"
SEED_PERSONA_TEXT = (
    "You are a chaotic, good-natured humor bot posting in a friends' Slack. "
    "Write one short, absurd shitpost (a sentence or two) in a deadpan "
    "voice. Never name or target real people. Keep it playful and safe "
    "for work."
)


def upgrade() -> None:
    op.create_table(
        "shitposterpersona",
        sa.Column("name", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
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

    op.create_table(
        "shitposterpersonarevision",
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column(
            "persona_text", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "cause_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column(
            "cause_ref", sqlmodel.sql.sqltypes.AutoString(), nullable=True
        ),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "valid_from",
            sa.DateTime(timezone=True),
            server_default=sa.text("NOW()"),
            nullable=False,
        ),
        sa.Column("valid_to", sa.DateTime(timezone=True), nullable=True),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.PrimaryKeyConstraint("id"),
        schema="fcm",
    )
    op.create_index(
        op.f("ix_fcm_shitposterpersonarevision_persona_id"),
        "shitposterpersonarevision",
        ["persona_id"],
        unique=False,
        schema="fcm",
    )
    op.create_index(
        "uq_shitposterpersonarevision_current",
        "shitposterpersonarevision",
        ["persona_id"],
        unique=True,
        schema="fcm",
        postgresql_where=sa.text("valid_to IS NULL"),
        sqlite_where=sa.text("valid_to IS NULL"),
    )

    op.create_table(
        "shitposterpost",
        sa.Column("slack_channel_id", sa.Integer(), nullable=False),
        sa.Column(
            "slack_message_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column("thread_ts", sqlmodel.sql.sqltypes.AutoString(), nullable=True),
        sa.Column("persona_id", sa.Integer(), nullable=False),
        sa.Column("persona_revision_id", sa.Integer(), nullable=False),
        sa.Column("trigger", sqlmodel.sql.sqltypes.AutoString(), nullable=False),
        sa.Column(
            "principal_iss", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "principal_sub", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "principal_kind", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "whagent_session_id", sqlmodel.sql.sqltypes.AutoString(), nullable=False
        ),
        sa.Column(
            "thread_owner_slack_user_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=True,
        ),
        sa.Column("parent_post_id", sa.Integer(), nullable=True),
        sa.Column("id", sa.Integer(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            server_default=sa.text("CURRENT_TIMESTAMP"),
            nullable=False,
        ),
        sa.CheckConstraint(
            "trigger IN ('scheduled', 'summon', 'riff')",
            name="ck_shitposterpost_trigger",
        ),
        sa.CheckConstraint(
            "principal_kind IN ('service', 'human')",
            name="ck_shitposterpost_principal_kind",
        ),
        sa.ForeignKeyConstraint(["slack_channel_id"], ["fcm.slackchannel.id"]),
        sa.ForeignKeyConstraint(["persona_id"], ["fcm.shitposterpersona.id"]),
        sa.ForeignKeyConstraint(
            ["persona_revision_id"], ["fcm.shitposterpersonarevision.id"]
        ),
        sa.ForeignKeyConstraint(["parent_post_id"], ["fcm.shitposterpost.id"]),
        sa.PrimaryKeyConstraint("id"),
        sa.UniqueConstraint(
            "slack_channel_id",
            "slack_message_ts",
            name="uq_shitposterpost_channel_ts",
        ),
        schema="fcm",
    )
    for column in (
        "slack_channel_id",
        "persona_id",
        "persona_revision_id",
        "parent_post_id",
    ):
        op.create_index(
            op.f(f"ix_fcm_shitposterpost_{column}"),
            "shitposterpost",
            [column],
            unique=False,
            schema="fcm",
        )

    # seed the one fixed persona and its initial revision
    persona = sa.table(
        "shitposterpersona", sa.column("id", sa.Integer), sa.column("name"), schema="fcm"
    )
    revision = sa.table(
        "shitposterpersonarevision",
        sa.column("persona_id", sa.Integer),
        sa.column("persona_text"),
        schema="fcm",
    )
    op.bulk_insert(persona, [{"id": 1, "name": SEED_PERSONA_NAME}])
    op.bulk_insert(
        revision, [{"persona_id": 1, "persona_text": SEED_PERSONA_TEXT}]
    )
    # keep the sequence ahead of the explicit seed id on Postgres
    if op.get_bind().dialect.name == "postgresql":
        op.execute(
            "SELECT setval(pg_get_serial_sequence('fcm.shitposterpersona', 'id'), "
            "(SELECT MAX(id) FROM fcm.shitposterpersona))"
        )


def downgrade() -> None:
    for column in (
        "parent_post_id",
        "persona_revision_id",
        "persona_id",
        "slack_channel_id",
    ):
        op.drop_index(
            op.f(f"ix_fcm_shitposterpost_{column}"),
            table_name="shitposterpost",
            schema="fcm",
        )
    op.drop_table("shitposterpost", schema="fcm")
    op.drop_index(
        "uq_shitposterpersonarevision_current",
        table_name="shitposterpersonarevision",
        schema="fcm",
    )
    op.drop_index(
        op.f("ix_fcm_shitposterpersonarevision_persona_id"),
        table_name="shitposterpersonarevision",
        schema="fcm",
    )
    op.drop_table("shitposterpersonarevision", schema="fcm")
    op.drop_table("shitposterpersona", schema="fcm")
