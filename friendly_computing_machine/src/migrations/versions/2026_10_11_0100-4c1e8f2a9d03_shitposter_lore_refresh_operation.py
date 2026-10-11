"""shitposter_lore_refresh_operation

Widen the memory-change operation CHECK constraint with 'refresh', written when
the idea-generator recomputes a generated lore entry's earned popularity.

Revision ID: 4c1e8f2a9d03
Revises: a4c7e2f9b1d3
Create Date: 2026-10-11 00:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "4c1e8f2a9d03"
down_revision: Union[str, Sequence[str], None] = "a4c7e2f9b1d3"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

NAME = "ck_shitpostermemorychange_operation"
TABLE = "shitpostermemorychange"
OLD = "operation IN ('add', 'reinforce', 'retire', 'merge', 'fold', 'promote')"
NEW = (
    "operation IN ('add', 'reinforce', 'retire', 'merge', 'fold', 'promote', "
    "'refresh')"
)


def upgrade() -> None:
    op.drop_constraint(NAME, TABLE, type_="check", schema="fcm")
    op.create_check_constraint(NAME, TABLE, NEW, schema="fcm")


def downgrade() -> None:
    op.drop_constraint(NAME, TABLE, type_="check", schema="fcm")
    op.create_check_constraint(NAME, TABLE, OLD, schema="fcm")
