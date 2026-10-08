"""merge_draft_and_operator_heads

Revision ID: f3c8a1d7e9b2
Revises: c5f9a2e7d1b4, ec90c2744356
Create Date: 2026-10-08 00:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "f3c8a1d7e9b2"
down_revision: Union[str, Sequence[str], None] = (
    "c5f9a2e7d1b4",
    "ec90c2744356",
)
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
