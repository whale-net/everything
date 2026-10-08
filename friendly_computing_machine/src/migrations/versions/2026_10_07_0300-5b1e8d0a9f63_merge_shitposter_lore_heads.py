"""merge shitposter memory and post engagement heads

Revision ID: 5b1e8d0a9f63
Revises: e5b0d3a8c2f5, f6c1a9d4e2b7
Create Date: 2026-10-07 03:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "5b1e8d0a9f63"
down_revision: Union[str, Sequence[str], None] = (
    "e5b0d3a8c2f5",
    "f6c1a9d4e2b7",
)
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
