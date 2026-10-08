"""merge_lore_head2

Revision ID: 682dd6ba8de4
Revises: 0da6a854779d, 7c3e9a1f5d20
Create Date: 2026-10-07 07:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "682dd6ba8de4"
down_revision: Union[str, Sequence[str], None] = ("0da6a854779d", "7c3e9a1f5d20")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
