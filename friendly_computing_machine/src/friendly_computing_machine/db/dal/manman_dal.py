"""ManMan model DAL functions."""

import logging
from typing import Optional

from sqlalchemy.dialects.postgresql import insert
from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
    db_update,
)
from friendly_computing_machine.src.friendly_computing_machine.models.manman import (
    ManManStatusUpdate,
    ManManStatusUpdateCreate,
)

logger = logging.getLogger(__name__)


def insert_manman_status_update(
    manman_status_update: ManManStatusUpdateCreate, session: Optional[Session] = None
) -> ManManStatusUpdate:
    """Insert a new ManMan status update."""
    with SessionManager(session) as session:
        db_manman_status_update = ManManStatusUpdate.model_validate(
            manman_status_update
        )
        session.add(db_manman_status_update)
        session.commit()
        session.refresh(db_manman_status_update)
        return db_manman_status_update


def upsert_manman_status_update(
    manman_status_update: ManManStatusUpdateCreate, session: Optional[Session] = None
) -> ManManStatusUpdate:
    """Insert or update a ManMan status update based on unique constraint.

    This is an atomic operation that handles race conditions where messages arrive out of order.
    Uses PostgreSQL's ON CONFLICT clause with SQLAlchemy 2.0 idioms.
    """
    with SessionManager(session) as session:
        # Convert to dict excluding unset fields
        values_dict = manman_status_update.model_dump(exclude_unset=True)

        # Build the insert statement with ON CONFLICT DO UPDATE
        insert_stmt = insert(ManManStatusUpdate).values(**values_dict)

        # For the update, exclude the constraint columns
        update_dict = {
            k: v
            for k, v in values_dict.items()
            if k not in ["service_type", "service_id"]
        }
        # Handle conflict on (service_type, service_id) unique constraint
        # Only update if the new as_of is later than the existing one
        update_stmt = insert_stmt.on_conflict_do_update(
            index_elements=["service_type", "service_id"],
            set_=update_dict,
            where=(ManManStatusUpdate.as_of < insert_stmt.excluded.as_of),
        ).returning(ManManStatusUpdate)

        # Execute the statement and get the result
        result = session.exec(update_stmt).first()

        if not result:
            raise RuntimeError("Upsert operation failed to return a result")
        # it's a row so we are hopefully getting the first one
        ret_res = result[0]
        logger.info("this is the result row %s", ret_res)
        session.commit()
        session.refresh(ret_res)
        return ret_res


def get_manman_status_update_by_id(
    manman_status_update_id: int, session: Optional[Session] = None
) -> ManManStatusUpdate | None:
    """Get a ManMan status update by ID."""
    with SessionManager(session) as session:
        return session.get(ManManStatusUpdate, manman_status_update_id)


def get_manman_status_update_from_create(
    manman_status_update_create: ManManStatusUpdateCreate,
    session: Optional[Session] = None,
) -> ManManStatusUpdate:
    """Get a ManMan status update from a ManManStatusUpdateCreate instance."""
    with SessionManager(session) as session:
        stmt = select(ManManStatusUpdate).where(
            ManManStatusUpdate.service_type == manman_status_update_create.service_type,
            ManManStatusUpdate.service_id == manman_status_update_create.service_id,
        )
        code = session.exec(stmt).first()
        if not code:
            raise ValueError(
                f"No ManManStatusUpdate found for service_type={manman_status_update_create.service_type} "
                f"and service_id={manman_status_update_create.service_id}"
            )
        logger.info("found ManManStatusUpdate.id=%s", code.id)
        return code


def get_manman_status_updates(
    session: Optional[Session] = None, skip: int = 0, limit: int = 100
) -> list[ManManStatusUpdate]:
    """Get ManMan status updates with pagination."""
    with SessionManager(session) as session:
        stmt = select(ManManStatusUpdate).offset(skip).limit(limit)
        return list(session.exec(stmt).all())


def update_manman_status_update(
    manman_status_update: ManManStatusUpdate,
    session: Optional[Session] = None,
) -> ManManStatusUpdate | None:
    """Update a ManMan status update.

    Args:
        manman_status_update: ManManStatusUpdate object with the changes to apply.
                             Only the fields that have been set (exclude_unset=True) will be updated.
        session: Optional database session

    Returns:
        The updated ManManStatusUpdate object or None if not found
    """
    with SessionManager(session) as session:
        update_dict = manman_status_update.model_dump(exclude_unset=True)

        manman_status_update = db_update(
            session, ManManStatusUpdate, manman_status_update.id, update_dict
        )
    return manman_status_update


def delete_manman_status_update(
    manman_status_update_id: int, session: Optional[Session] = None
) -> bool:
    """Delete a ManMan status update."""
    with SessionManager(session) as session:
        manman_status_update = session.get(ManManStatusUpdate, manman_status_update_id)
        if manman_status_update:
            session.delete(manman_status_update)
            session.commit()
            return True
        return False
