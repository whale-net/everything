"""merge_duplicate_snapshot_reflector_merges

Joins the two equivalent merge heads (9e4b7d2a6c18 from main, a4f1d8c3e7b2 from
the snapshot trigger branch) so the migration graph has exactly one head.

Revision ID: b3d8f0a6e1c5
Revises: 9e4b7d2a6c18, a4f1d8c3e7b2, 4c1a9e7d3b52
Create Date: 2026-10-07 10:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "b3d8f0a6e1c5"
down_revision: Union[str, Sequence[str], None] = ("9e4b7d2a6c18", "a4f1d8c3e7b2", "4c1a9e7d3b52")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
