"""merge_posts_snapshot_heads

Joins the main-line merge head (9e4b7d2a6c18) with the post context snapshot
migration (b3d8f1e6a2c9) so the migration graph has exactly one head.

Revision ID: 4c1a9e7d3b52
Revises: 9e4b7d2a6c18, b3d8f1e6a2c9
Create Date: 2026-10-07 10:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "4c1a9e7d3b52"
down_revision: Union[str, Sequence[str], None] = ("9e4b7d2a6c18", "b3d8f1e6a2c9")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
