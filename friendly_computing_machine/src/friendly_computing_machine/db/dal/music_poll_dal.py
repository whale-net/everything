"""Music Poll model DAL functions."""

import datetime
import logging
from typing import Optional

from sqlmodel import Session, and_, exists, not_, null, select

from friendly_computing_machine.db.util import SessionManager, db_update
from friendly_computing_machine.models.music_poll import (
    MusicPoll,
    MusicPollCreate,
    MusicPollInstance,
    MusicPollInstanceCreate,
    MusicPollResponse,
    MusicPollResponseCreate,
)

logger = logging.getLogger(__name__)


def insert_music_poll(
    music_poll: MusicPollCreate, session: Optional[Session] = None
) -> MusicPoll:
    """Insert a new music poll."""
    with SessionManager(session) as session:
        db_music_poll = music_poll.to_music_poll()
        session.add(db_music_poll)
        session.commit()
        session.refresh(db_music_poll)
        return db_music_poll


def get_music_poll_by_id(
    music_poll_id: int, session: Optional[Session] = None
) -> MusicPoll | None:
    """Get a music poll by ID."""
    with SessionManager(session) as session:
        return session.get(MusicPoll, music_poll_id)


def get_music_polls(
    session: Optional[Session] = None, skip: int = 0, limit: int = 100
) -> list[MusicPoll]:
    """Get music polls with pagination."""
    with SessionManager(session) as session:
        stmt = select(MusicPoll).offset(skip).limit(limit)
        return list(session.exec(stmt).all())


def update_music_poll(
    music_poll_id: int, updates: dict[str, any], session: Optional[Session] = None
) -> MusicPoll | None:
    """Update a music poll."""
    with SessionManager(session) as session:
        music_poll = db_update(session, MusicPoll, music_poll_id, updates)
    return music_poll


def delete_music_poll(music_poll_id: int, session: Optional[Session] = None) -> bool:
    """Delete a music poll."""
    with SessionManager(session) as session:
        music_poll = session.get(MusicPoll, music_poll_id)
        if music_poll:
            session.delete(music_poll)
            session.commit()
            return True
        return False


def insert_music_poll_instance(
    instance: MusicPollInstanceCreate, session: Optional[Session] = None
) -> MusicPollInstance:
    """Insert a new music poll instance."""
    with SessionManager(session) as session:
        db_instance = instance.to_music_poll_instance()
        session.add(db_instance)
        session.commit()
        session.refresh(db_instance)
        return db_instance


def get_music_poll_instance_by_id(
    instance_id: int, session: Optional[Session] = None
) -> MusicPollInstance | None:
    """Get a music poll instance by ID."""
    with SessionManager(session) as session:
        return session.get(MusicPollInstance, instance_id)


def get_music_poll_instances(
    music_poll_id: int, session: Optional[Session] = None
) -> list[MusicPollInstance]:
    """Get all instances for a music poll."""
    with SessionManager(session) as session:
        stmt = select(MusicPollInstance).where(
            MusicPollInstance.music_poll_id == music_poll_id
        )
        return list(session.exec(stmt).all())


def get_unprocessed_music_poll_instances(
    in_session: Optional[Session] = None,
) -> list[MusicPollInstance]:
    """Get unprocessed music poll instances."""
    with SessionManager(in_session) as session:
        stmt = select(MusicPollInstance).where(
            and_(
                MusicPollInstance.next_instance_id is not null(),
                not_(
                    exists().where(
                        MusicPollResponse.music_poll_instance_id == MusicPollInstance.id
                    )
                ),
            )
        )
        return list(session.exec(stmt).all())


def get_recent_music_poll_instances(
    in_session: Optional[Session] = None,
    delta: datetime.timedelta = datetime.timedelta(days=10),
) -> list[MusicPollInstance]:
    """Get recent music poll instances within a time delta."""
    with SessionManager(in_session) as session:
        stmt = select(MusicPollInstance).where(
            MusicPollInstance.created_at >= datetime.datetime.now() - delta
        )
        return list(session.exec(stmt).all())


def update_music_poll_instance(
    instance_id: int, updates: dict, session: Optional[Session] = None
) -> MusicPollInstance | None:
    """Update a music poll instance."""
    with SessionManager(session) as session:
        instance = db_update(session, MusicPollInstance, instance_id, updates)
    return instance


def delete_music_poll_instance(
    instance_id: int, session: Optional[Session] = None
) -> bool:
    """Delete a music poll instance."""
    with SessionManager(session) as session:
        instance = session.get(MusicPollInstance, instance_id)
        if instance:
            session.delete(instance)
            session.commit()
            return True
        return False


def insert_music_poll_responses(
    responses: list[MusicPollResponseCreate], session: Optional[Session] = None
):
    """Insert multiple music poll responses."""
    with SessionManager(session) as session:
        db_responses = [
            MusicPollResponse.model_validate(response) for response in responses
        ]
        session.bulk_save_objects(db_responses)
        session.commit()
        # for response in db_responses:
        #     session.refresh(response)
        # return db_responses


def insert_music_poll_response(
    response: MusicPollResponseCreate, session: Optional[Session] = None
) -> MusicPollResponse:
    """Insert a new music poll response."""
    with SessionManager(session) as session:
        db_response = response.to_music_poll_response()
        session.add(db_response)
        session.commit()
        session.refresh(db_response)
        return db_response


def get_music_poll_response_by_id(
    response_id: int, session: Optional[Session] = None
) -> MusicPollResponse | None:
    """Get a music poll response by ID."""
    with SessionManager(session) as session:
        return session.get(MusicPollResponse, response_id)


def get_music_poll_responses(
    instance_id: int, session: Optional[Session] = None
) -> list[MusicPollResponse]:
    """Get all responses for a music poll instance."""
    with SessionManager(session) as session:
        stmt = select(MusicPollResponse).where(
            MusicPollResponse.music_poll_instance_id == instance_id
        )
        return list(session.exec(stmt).all())


def update_music_poll_response(
    response_id: int, updates: dict, session: Optional[Session] = None
) -> MusicPollResponse | None:
    """Update a music poll response."""
    with SessionManager(session) as session:
        response = db_update(session, MusicPollResponse, response_id, updates)
    return response


def delete_music_poll_response(
    response_id: int, session: Optional[Session] = None
) -> bool:
    """Delete a music poll response."""
    with SessionManager(session) as session:
        response = session.get(MusicPollResponse, response_id)
        if response:
            session.delete(response)
            session.commit()
            return True
        return False
