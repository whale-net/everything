"""merge reflector base heads

Revision ID: 2a73c1430e44
Revises: a7d3e0b9c4f1, f6c1a9d4e2b7, e5b0d3a8c2f5
Create Date: 2026-10-07 03:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "2a73c1430e44"
down_revision: Union[str, Sequence[str], None] = (
    "a7d3e0b9c4f1",
    "f6c1a9d4e2b7",
    "e5b0d3a8c2f5",
)
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
