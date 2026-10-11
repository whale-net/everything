"""shitposter_idea_generator_kinds

Widen the brain job kind, lore kind and memory-change cause kind CHECK
constraints for the lore idea-generator.

Revision ID: d7a3e9c1b4f6
Revises: f3c8a1d7e9b2
Create Date: 2026-10-10 00:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "d7a3e9c1b4f6"
down_revision: Union[str, Sequence[str], None] = "f3c8a1d7e9b2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

# (constraint name, table, old condition, new condition)
_CONSTRAINTS = [
    (
        "ck_shitposterbrainjobrun_job_kind",
        "shitposterbrainjobrun",
        "job_kind IN ('harvest', 'reflect', 'write', 'snapshot')",
        "job_kind IN ('harvest', 'reflect', 'write', 'snapshot', 'idea_generate')",
    ),
    (
        "ck_shitposterloreentry_kind",
        "shitposterloreentry",
        "kind IN ('hit', 'consolidated')",
        "kind IN ('hit', 'consolidated', 'theme', 'novel')",
    ),
    (
        "ck_shitpostermemorychange_cause_kind",
        "shitpostermemorychange",
        "cause_kind IN ('post_engagement', 'suggestion', 'operator', 'fold')",
        "cause_kind IN ('post_engagement', 'suggestion', 'operator', 'fold', "
        "'theme_synthesis', 'novel_gate')",
    ),
]


def upgrade() -> None:
    for name, table, _old, new in _CONSTRAINTS:
        op.drop_constraint(name, table, type_="check", schema="fcm")
        op.create_check_constraint(name, table, new, schema="fcm")


def downgrade() -> None:
    for name, table, old, _new in _CONSTRAINTS:
        op.drop_constraint(name, table, type_="check", schema="fcm")
        op.create_check_constraint(name, table, old, schema="fcm")
