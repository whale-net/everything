"""merge_operator_and_main_heads

Revision ID: ec90c2744356
Revises: e4a7c0d2b913, 9e4b7d2a6c18, 5c1d8e3f7a2b
Create Date: 2026-10-07 11:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "ec90c2744356"
down_revision: Union[str, Sequence[str], None] = (
    "e4a7c0d2b913",
    "9e4b7d2a6c18",
    "5c1d8e3f7a2b",
)
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
