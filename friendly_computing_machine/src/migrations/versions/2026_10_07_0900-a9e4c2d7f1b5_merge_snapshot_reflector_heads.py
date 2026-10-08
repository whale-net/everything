"""merge_snapshot_reflector_heads

Joins the reflector run chain (8d373d357fe9) with the context snapshot chain
(c8e2f5a1d9b3) so the migration graph has exactly one head.

Revision ID: a9e4c2d7f1b5
Revises: 8d373d357fe9, c8e2f5a1d9b3
Create Date: 2026-10-07 09:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "a9e4c2d7f1b5"
down_revision: Union[str, Sequence[str], None] = ("8d373d357fe9", "c8e2f5a1d9b3")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
