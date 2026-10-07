"""merge_suggestion_intake_head

Revision ID: 2ed62c2b6b82
Revises: e5b0d3a8c2f4, e5b1f8a2c9d4
Create Date: 2026-10-07 04:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "2ed62c2b6b82"
down_revision: Union[str, Sequence[str], None] = ("e5b0d3a8c2f4", "e5b1f8a2c9d4")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
