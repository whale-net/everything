"""merge_snapshot_reflector_heads

Joins the context snapshot merge head (c8e2f5a1d9b3) with the reflector run
merge head (8d373d357fe9) so the migration graph has exactly one head.

Revision ID: 9e4b7d2a6c18
Revises: c8e2f5a1d9b3, 8d373d357fe9
Create Date: 2026-10-07 09:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "9e4b7d2a6c18"
down_revision: Union[str, Sequence[str], None] = ("c8e2f5a1d9b3", "8d373d357fe9")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
