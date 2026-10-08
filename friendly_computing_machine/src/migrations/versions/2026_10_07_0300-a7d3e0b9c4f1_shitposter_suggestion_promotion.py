"""shitposter_suggestion_promotion

Revision ID: a7d3e0b9c4f1
Revises: f6c2a9d1e8b3
Create Date: 2026-10-07 03:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
import sqlmodel
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "a7d3e0b9c4f1"
down_revision: Union[str, None] = "f6c2a9d1e8b3"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.add_column(
        "shitpostersuggestion",
        sa.Column("promoted_at", sa.DateTime(timezone=True), nullable=True),
        schema="fcm",
    )
    op.add_column(
        "shitpostersuggestion",
        sa.Column(
            "consumed_by_reflector_run_id",
            sqlmodel.sql.sqltypes.AutoString(),
            nullable=True,
        ),
        schema="fcm",
    )


def downgrade() -> None:
    op.drop_column(
        "shitpostersuggestion", "consumed_by_reflector_run_id", schema="fcm"
    )
    op.drop_column("shitpostersuggestion", "promoted_at", schema="fcm")
