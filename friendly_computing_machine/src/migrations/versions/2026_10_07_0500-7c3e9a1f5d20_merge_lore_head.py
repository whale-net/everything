"""merge_lore_head

Revision ID: 7c3e9a1f5d20
Revises: 5b1e8d0a9f63, 2ed62c2b6b82
Create Date: 2026-10-07 05:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "7c3e9a1f5d20"
down_revision: Union[str, Sequence[str], None] = ("5b1e8d0a9f63", "2ed62c2b6b82")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
