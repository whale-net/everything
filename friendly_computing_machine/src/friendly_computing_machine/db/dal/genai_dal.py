"""GenAI model DAL functions."""

import datetime
import logging
from typing import Optional

from sqlmodel import Session, select

from friendly_computing_machine.db.util import SessionManager
from friendly_computing_machine.models.genai import GenAIText, GenAITextCreate

logger = logging.getLogger(__name__)


def insert_genai_text(
    genai_text: GenAITextCreate,
    session: Optional[Session] = None,
) -> GenAIText:
    """Insert a new GenAI text."""
    with SessionManager(session) as session:
        # db_genai_text = GenAIText.model_validate(genai_text)
        db_genai_text = genai_text.to_genai_text()
        session.add(db_genai_text)
        session.commit()
        session.refresh(db_genai_text)
        return db_genai_text


def get_genai_texts(
    session: Optional[Session] = None, skip: int = 0, limit: int = 100
) -> list[GenAIText]:
    """Get GenAI texts with pagination."""
    with SessionManager(session) as session:
        stmt = select(GenAIText).offset(skip).limit(limit)
        return list(session.exec(stmt).all())


def get_genai_texts_by_slack_channel(
    slack_channel_slack_id: str, limit: int = 10, session: Optional[Session] = None
) -> list[GenAIText]:
    """Get GenAI texts for a specific Slack channel."""
    with SessionManager(session) as session:
        stmt = (
            select(GenAIText)
            .where(GenAIText.slack_channel_slack_id == slack_channel_slack_id)
            .order_by(GenAIText.created_at.desc())
            .limit(limit)
        )
        return list(session.exec(stmt).all())


def get_genai_text_by_id(
    genai_text_id: int, session: Optional[Session] = None
) -> GenAIText | None:
    """Get a GenAI text by ID."""
    with SessionManager(session) as session:
        stmt = select(GenAIText).where(GenAIText.id == genai_text_id)
        return session.exec(stmt).one_or_none()


def update_genai_text_response(
    genai_text_id: int,
    response: str,
    response_as_of: datetime.datetime = None,
    session: Optional[Session] = None,
) -> GenAIText | None:
    """Update the response for a GenAI text."""
    # TODO - this is ai generated. should this also take in or have the option to take in the genAI object?
    with SessionManager(session) as session:
        genai_text = session.get(GenAIText, genai_text_id)
        if genai_text:
            genai_text.response = response
            genai_text.response_as_of = response_as_of or datetime.datetime.now()
            session.add(genai_text)
            session.commit()
            session.refresh(genai_text)
        return genai_text
