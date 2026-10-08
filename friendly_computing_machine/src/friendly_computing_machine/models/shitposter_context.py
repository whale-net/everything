"""Shitposter context snapshots: versioned, append-only ranked memory per persona.

Each snapshot names the brain job run that wrote it. Item rows point at the
SCD2 attribute or lore row version they included, so a later snapshot can
tell whether an item is still current.
"""

import datetime

from sqlalchemy import CheckConstraint, Column, DateTime, Index, UniqueConstraint, func
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class ShitposterContextSnapshot(Base, table=True):
    __table_args__ = (
        UniqueConstraint(
            "persona_id", "version", name="uq_shitpostercontextsnapshot_persona_version"
        ),
        CheckConstraint("version >= 1", name="ck_shitpostercontextsnapshot_version"),
        CheckConstraint(
            "token_count >= 0", name="ck_shitpostercontextsnapshot_token_count"
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    version: int = Field(nullable=False)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    brain_job_run_id: int = Field(
        nullable=False, foreign_key="shitposterbrainjobrun.id"
    )
    token_count: int = Field(nullable=False)
    rendered_text: str


class ShitposterSnapshotItem(Base, table=True):
    __table_args__ = (
        Index("ix_shitpostersnapshotitem_item", "item_kind", "item_id"),
        CheckConstraint(
            "item_kind IN ('attribute', 'lore')",
            name="ck_shitpostersnapshotitem_item_kind",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    snapshot_id: int = Field(
        nullable=False, foreign_key="shitpostercontextsnapshot.id", index=True
    )
    # ShitposterMemoryEntityKindEnum value
    item_kind: str
    # id of the SCD2 attribute or lore row version included in the snapshot
    item_id: int = Field(nullable=False)
    rank: int = Field(nullable=False)
    is_random_pick: bool = Field(default=False, nullable=False)
