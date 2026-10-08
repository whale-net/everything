"""merge_memory_harvester_heads

Revision ID: 3f3e315f95d4
Revises: 2caf78bf882b, 7c3e9a1f5d28
Create Date: 2026-10-07 06:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "3f3e315f95d4"
down_revision: Union[str, Sequence[str], None] = ("2caf78bf882b", "7c3e9a1f5d28")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
