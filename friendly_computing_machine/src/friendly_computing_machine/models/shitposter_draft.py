"""Shitposter draft queue: posts written ahead of their scheduled slot.

Each draft names the context snapshot it was written from and the write job run
that produced it. A draft is used at most once: `used_at` and `used_post_id` are
set in the same transaction as the post record. `discarded_at` marks a draft
dropped at posting time (guardrail failure, retired item, or expiry).
"""

import datetime

from sqlalchemy import CheckConstraint, Column, DateTime, Index, func, text
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (  # noqa: F401
    ShitposterBrainJobRun,
    ShitposterPost,
    ShitposterPersona,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (  # noqa: F401
    ShitposterContextSnapshot,
)


class ShitposterDraft(Base, table=True):
    __table_args__ = (
        CheckConstraint("rank >= 1", name="ck_shitposterdraft_rank"),
        # the scheduled-post path scans a persona's unused drafts
        Index(
            "ix_shitposterdraft_unused",
            "persona_id",
            "rank",
            postgresql_where=text("used_at IS NULL AND discarded_at IS NULL"),
            sqlite_where=text("used_at IS NULL AND discarded_at IS NULL"),
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    snapshot_id: int = Field(
        nullable=False, foreign_key="shitpostercontextsnapshot.id", index=True
    )
    brain_job_run_id: int = Field(
        nullable=False, foreign_key="shitposterbrainjobrun.id"
    )
    text: str = Field(nullable=False)
    # writer-assigned ranking; lower is better
    rank: int = Field(nullable=False)
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    used_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    used_post_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterpost.id", unique=True
    )
    discarded_at: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )
    discard_reason: str | None = Field(default=None, nullable=True)
