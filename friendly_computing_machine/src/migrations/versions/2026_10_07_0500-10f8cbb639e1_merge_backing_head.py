"""merge_backing_head

Revision ID: 10f8cbb639e1
Revises: a7d3e0b9c4f1, 2ed62c2b6b82
Create Date: 2026-10-07 05:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "10f8cbb639e1"
down_revision: Union[str, Sequence[str], None] = ("a7d3e0b9c4f1", "2ed62c2b6b82")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
