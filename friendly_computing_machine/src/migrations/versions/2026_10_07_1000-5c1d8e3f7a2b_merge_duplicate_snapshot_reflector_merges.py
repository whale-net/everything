"""merge_duplicate_snapshot_reflector_merges

Joins the merge heads from main (9e4b7d2a6c18, b3d8f0a6e1c5) and this
branch (a9e4c2d7f1b5) over the reflector and context snapshot chains, so the
migration graph keeps exactly one head.

Revision ID: 5c1d8e3f7a2b
Revises: 9e4b7d2a6c18, a9e4c2d7f1b5, b3d8f0a6e1c5
Create Date: 2026-10-07 10:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "5c1d8e3f7a2b"
down_revision: Union[str, Sequence[str], None] = (
    "9e4b7d2a6c18",
    "a9e4c2d7f1b5",
    "b3d8f0a6e1c5",
)
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
