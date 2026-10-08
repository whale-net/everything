"""merge_context_snapshot_head

Joins the context snapshot chain (f6c1a9d2e8b4) with the lore/memory chain
(682dd6ba8de4) so the migration graph has exactly one head.

Revision ID: c8e2f5a1d9b3
Revises: f6c1a9d2e8b4, 682dd6ba8de4
Create Date: 2026-10-07 08:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "c8e2f5a1d9b3"
down_revision: Union[str, Sequence[str], None] = ("f6c1a9d2e8b4", "682dd6ba8de4")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
