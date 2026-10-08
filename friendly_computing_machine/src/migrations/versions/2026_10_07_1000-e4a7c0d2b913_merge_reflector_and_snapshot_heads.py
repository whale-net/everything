"""merge_reflector_and_snapshot_heads

Revision ID: e4a7c0d2b913
Revises: 8d373d357fe9, b3d8f1e6a2c9
Create Date: 2026-10-07 10:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "e4a7c0d2b913"
down_revision: Union[str, Sequence[str], None] = ("8d373d357fe9", "b3d8f1e6a2c9")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
