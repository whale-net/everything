"""merge_harvester_head2

Revision ID: 7c3e9a1f5d28
Revises: f6c1a9d4e2b7, 2ed62c2b6b82
Create Date: 2026-10-07 05:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "7c3e9a1f5d28"
down_revision: Union[str, Sequence[str], None] = ("f6c1a9d4e2b7", "2ed62c2b6b82")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
