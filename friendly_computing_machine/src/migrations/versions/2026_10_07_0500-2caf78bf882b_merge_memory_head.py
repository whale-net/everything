"""merge_memory_head

Revision ID: 2caf78bf882b
Revises: a7c3e9f1b2d4, 2ed62c2b6b82
Create Date: 2026-10-07 05:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "2caf78bf882b"
down_revision: Union[str, Sequence[str], None] = ("a7c3e9f1b2d4", "2ed62c2b6b82")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
