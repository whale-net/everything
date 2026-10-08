"""Shitposter learned memory, keyed by persona id.

Attributes and lore are SCD2 (valid_to NULL = current). The change log is
append-only and every row names at least one cause.
"""

import datetime
from enum import Enum

from sqlalchemy import CheckConstraint, Column, DateTime, Index, func, text
from sqlmodel import Field

from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class ShitposterAttributeStatusEnum(str, Enum):
    ACTIVE = "active"
    RETIRED = "retired"
    MERGED = "merged"


class ShitposterLoreKindEnum(str, Enum):
    HIT = "hit"
    CONSOLIDATED = "consolidated"


class ShitposterMemoryEntityKindEnum(str, Enum):
    ATTRIBUTE = "attribute"
    LORE = "lore"


class ShitposterMemoryOperationEnum(str, Enum):
    ADD = "add"
    REINFORCE = "reinforce"
    RETIRE = "retire"
    MERGE = "merge"
    FOLD = "fold"
    PROMOTE = "promote"


class ShitposterMemoryCauseKindEnum(str, Enum):
    POST_ENGAGEMENT = "post_engagement"
    SUGGESTION = "suggestion"
    OPERATOR = "operator"
    FOLD = "fold"


class ShitposterPersonaAttribute(Base, table=True):
    __table_args__ = (
        # at most one current row per (persona, attribute key)
        Index(
            "uq_shitposterpersonaattribute_current",
            "persona_id",
            "attribute_key",
            unique=True,
            postgresql_where=text("valid_to IS NULL"),
            sqlite_where=text("valid_to IS NULL"),
        ),
        CheckConstraint(
            "status IN ('active', 'retired', 'merged')",
            name="ck_shitposterpersonaattribute_status",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    attribute_key: str
    text: str
    # ShitposterAttributeStatusEnum value
    status: str
    retired_by_operator: bool = Field(default=False, nullable=False)
    valid_from: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    valid_to: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterLoreEntry(Base, table=True):
    __table_args__ = (
        Index(
            "ix_shitposterloreentry_current",
            "persona_id",
            postgresql_where=text("valid_to IS NULL"),
            sqlite_where=text("valid_to IS NULL"),
        ),
        CheckConstraint(
            "kind IN ('hit', 'consolidated')",
            name="ck_shitposterloreentry_kind",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    text: str
    # ShitposterLoreKindEnum value
    kind: str
    source_post_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterpost.id"
    )
    popularity_score: int = Field(default=0, nullable=False)
    retired_by_operator: bool = Field(default=False, nullable=False)
    valid_from: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    valid_to: datetime.datetime | None = Field(
        default=None, sa_column=Column(DateTime(timezone=True), nullable=True)
    )


class ShitposterMemoryChange(Base, table=True):
    """Append-only log of every attribute and lore operation. Not SCD2."""

    __table_args__ = (
        CheckConstraint(
            "entity_kind IN ('attribute', 'lore')",
            name="ck_shitpostermemorychange_entity_kind",
        ),
        CheckConstraint(
            "operation IN ('add', 'reinforce', 'retire', 'merge', 'fold', 'promote')",
            name="ck_shitpostermemorychange_operation",
        ),
        CheckConstraint(
            "cause_kind IN ('post_engagement', 'suggestion', 'operator', 'fold')",
            name="ck_shitpostermemorychange_cause_kind",
        ),
        CheckConstraint(
            "cause_post_id IS NOT NULL OR cause_ref IS NOT NULL",
            name="ck_shitpostermemorychange_has_cause",
        ),
    )
    id: int = Field(default=None, nullable=False, primary_key=True)
    changed_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), nullable=False, server_default=func.now()
        ),
    )
    persona_id: int = Field(
        nullable=False, foreign_key="shitposterpersona.id", index=True
    )
    # ShitposterMemoryEntityKindEnum value
    entity_kind: str
    entity_id: int | None = Field(default=None, nullable=True)
    # ShitposterMemoryOperationEnum value
    operation: str
    before_text: str | None = Field(default=None, nullable=True)
    after_text: str | None = Field(default=None, nullable=True)
    # ShitposterMemoryCauseKindEnum value
    cause_kind: str
    cause_post_id: int | None = Field(
        default=None, nullable=True, foreign_key="shitposterpost.id"
    )
    # free-form cause reference: suggestion id, operator subject, or fold run
    cause_ref: str | None = Field(default=None, nullable=True)
    reflector_run_id: str | None = Field(default=None, nullable=True)
