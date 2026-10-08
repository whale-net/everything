"""merge_suggestion_memory_heads

Revision ID: 0da6a854779d
Revises: 10f8cbb639e1, 3f3e315f95d4
Create Date: 2026-10-07 06:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "0da6a854779d"
down_revision: Union[str, Sequence[str], None] = ("10f8cbb639e1", "3f3e315f95d4")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
