"""shitposter_lore_key

Add a stable lore_key shared by every SCD2 revision of one logical lore entry,
backfilled from existing revision chains.

Revision ID: b7039c37db2c
Revises: d7a3e9c1b4f6
Create Date: 2026-10-10 01:00:00.000000

"""

from typing import Sequence, Union

import sqlalchemy as sa
from alembic import op

# revision identifiers, used by Alembic.
revision: str = "b7039c37db2c"
down_revision: Union[str, Sequence[str], None] = "d7a3e9c1b4f6"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

TABLE = "shitposterloreentry"
INDEX = "ix_shitposterloreentry_persona_lore_key"


def _backfill(conn: sa.engine.Connection) -> None:
    """Give every revision of one logical lore entry the same key."""
    # Implementation lane: chain revisions and write deterministic keys.


def upgrade() -> None:
    op.add_column(
        TABLE, sa.Column("lore_key", sa.Uuid(), nullable=True), schema="fcm"
    )
    _backfill(op.get_bind())
    with op.batch_alter_table(TABLE, schema="fcm") as batch:
        batch.alter_column("lore_key", existing_type=sa.Uuid(), nullable=False)
    op.create_index(INDEX, TABLE, ["persona_id", "lore_key"], schema="fcm")


def downgrade() -> None:
    op.drop_index(INDEX, table_name=TABLE, schema="fcm")
    with op.batch_alter_table(TABLE, schema="fcm") as batch:
        batch.drop_column("lore_key")
