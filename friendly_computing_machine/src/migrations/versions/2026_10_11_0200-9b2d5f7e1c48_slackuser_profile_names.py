"""slackuser_profile_names

Store a Slack user's display_name and real_name separately so memory-write
guardrails can match either; name keeps its existing value.

Revision ID: 9b2d5f7e1c48
Revises: 4c1e8f2a9d03
Create Date: 2026-10-11 02:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "9b2d5f7e1c48"
down_revision: Union[str, Sequence[str], None] = "4c1e8f2a9d03"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.add_column(
        "slackuser", sa.Column("display_name", sa.String(), nullable=True), schema="fcm"
    )
    op.add_column(
        "slackuser", sa.Column("real_name", sa.String(), nullable=True), schema="fcm"
    )


def downgrade() -> None:
    op.drop_column("slackuser", "real_name", schema="fcm")
    op.drop_column("slackuser", "display_name", schema="fcm")
