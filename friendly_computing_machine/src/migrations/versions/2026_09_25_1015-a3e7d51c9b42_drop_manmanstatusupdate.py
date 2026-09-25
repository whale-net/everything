"""drop manmanstatusupdate

Revision ID: a3e7d51c9b42
Revises: c1d4e8a7b2f9
Create Date: 2026-09-25 10:15:00.000000

The manman V1 relay is gone, so its status snapshot goes with it. This table was
already a snapshot rather than a record: the `slack_message_id` column was
dropped back in `71e2c8de4b19`, so nothing here tracks a posted message any
more. Edit-in-place storage for routed messages is therefore built from scratch
by the next milestone's routing work -- do not treat this table as a template.

One-way door: applied migrations are never edited, so there is no downgrade that
brings the data back, and no helm pre-rollback hook exists to run one. Recovery
is restoring the database to a pre-M1 backup or applying a repair migration.
"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "a3e7d51c9b42"
down_revision: Union[str, None] = "c1d4e8a7b2f9"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    op.drop_table("manmanstatusupdate", schema="fcm")


def downgrade() -> None:
    # Irreversible by design: the rows this table held cannot be reconstructed
    # from what is left in the schema, so a downgrade would only fabricate an
    # empty table. Recovery is a backup restore or a hand-written repair
    # migration, not this hook.
    raise NotImplementedError(
        "manmanstatusupdate is dropped irreversibly; restore the database from a "
        "pre-M1 backup or write a repair forward migration."
    )
