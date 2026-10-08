"""merge shitposter brain job run and memory heads

Revision ID: a7c3e9f1b2d4
Revises: e5b0d3a8c2f4, e5b0d3a8c2f5
Create Date: 2026-10-07 02:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "a7c3e9f1b2d4"
down_revision: Union[str, Sequence[str], None] = ("e5b0d3a8c2f4", "e5b0d3a8c2f5")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
