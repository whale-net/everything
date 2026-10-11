"""shitposter_lore_key

Add a stable lore_key shared by every SCD2 revision of one logical lore entry,
backfilled from existing revision chains.

Revision ID: b7039c37db2c
Revises: d7a3e9c1b4f6
Create Date: 2026-10-10 01:00:00.000000

"""

import uuid
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


# keys derive from each chain's root row id, so reruns write the same values
KEY_NAMESPACE = uuid.UUID("5d0f2c1e-8a4b-4f3e-9c6d-7b1a2e3f4c5d")

# A revision link: a lore row closed by a reinforce/promote change whose
# successor (same persona and kind) opened at the instant it closed. Each
# row keeps at most one successor and one predecessor (lowest id wins).
_CHAIN_SQL = sa.text(
    """
    WITH RECURSIVE candidate AS (
        SELECT a.id AS prev_id, b.id AS next_id
        FROM fcm.shitposterloreentry a
        JOIN fcm.shitposterloreentry b
          ON b.persona_id = a.persona_id
         AND b.kind = a.kind
         AND b.valid_from = a.valid_to
         AND b.id <> a.id
        WHERE EXISTS (
            SELECT 1 FROM fcm.shitpostermemorychange c
            WHERE c.entity_kind = 'lore'
              AND c.entity_id = a.id
              AND c.persona_id = a.persona_id
              AND c.operation IN ('reinforce', 'promote')
        )
    ),
    first_next AS (
        SELECT prev_id, MIN(next_id) AS next_id FROM candidate GROUP BY prev_id
    ),
    edge AS (
        SELECT MIN(prev_id) AS prev_id, next_id FROM first_next GROUP BY next_id
    ),
    chain (id, root_id) AS (
        SELECT l.id, l.id
        FROM fcm.shitposterloreentry l
        WHERE NOT EXISTS (SELECT 1 FROM edge e WHERE e.next_id = l.id)
        UNION ALL
        SELECT e.next_id, chain.root_id
        FROM chain JOIN edge e ON e.prev_id = chain.id
    )
    SELECT l.id, COALESCE(chain.root_id, l.id) AS root_id
    FROM fcm.shitposterloreentry l
    LEFT JOIN chain ON chain.id = l.id
    ORDER BY l.id
    """
)


def lore_key_for_root(root_id: int) -> uuid.UUID:
    """Deterministic key for the chain rooted at lore row root_id."""
    return uuid.uuid5(KEY_NAMESPACE, f"shitposterloreentry:{root_id}")


def _backfill(conn: sa.engine.Connection) -> None:
    """Give every revision of one logical lore entry the same key."""
    rows = conn.execute(_CHAIN_SQL).all()
    if not rows:
        return
    lore = sa.table(
        TABLE, sa.column("id", sa.Integer()), sa.column("lore_key", sa.Uuid()), schema="fcm"
    )
    conn.execute(
        sa.update(lore)
        .where(lore.c.id == sa.bindparam("b_id"))
        .values(lore_key=sa.bindparam("b_key", type_=sa.Uuid())),
        [{"b_id": row_id, "b_key": lore_key_for_root(root_id)} for row_id, root_id in rows],
    )


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
