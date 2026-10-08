"""merge_reflector_run_head

Revision ID: 8d373d357fe9
Revises: b8e4f2a1c9d7, 682dd6ba8de4
Create Date: 2026-10-07 08:00:00.000000

"""

from typing import Sequence, Union

# revision identifiers, used by Alembic.
revision: str = "8d373d357fe9"
down_revision: Union[str, Sequence[str], None] = ("b8e4f2a1c9d7", "682dd6ba8de4")
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    pass


def downgrade() -> None:
    pass
