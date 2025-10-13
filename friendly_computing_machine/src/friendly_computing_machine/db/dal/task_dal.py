"""Task model DAL functions."""

import logging
from typing import Optional

from sqlmodel import Session, and_, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.task import (
    Task,
    TaskCreate,
    TaskInstance,
    TaskInstanceCreate,
    TaskInstanceStatus,
)

logger = logging.getLogger(__name__)


def upsert_tasks(tasks: list[TaskCreate]) -> list[Task]:
    """Insert or update multiple tasks."""
    # DO NOT USE?
    # RBAR BABY
    # I tried to look into upserts with sa, but didn't want to rabbit hole myself for minimal gain here
    with SessionManager() as session:
        out_tasks = []
        for task in tasks:
            out_tasks.append(upsert_task(task, session))
    return list(out_tasks)


def upsert_task(task: TaskCreate, session: Optional[Session] = None) -> Task:
    """Insert or update a task."""
    with SessionManager(session) as session:
        # check if exists and insert. very non-optimal, especially if rbar, but small table so no prob
        stmt = select(Task).where(Task.name == task.name)
        result = session.exec(stmt).one_or_none()
        if result is None:
            result = Task(name=task.name)
            session.add(result)
            session.commit()
            session.refresh(result)

    return result


def insert_task_instances(task_instances: list[TaskInstanceCreate]):
    """Insert multiple task instances."""
    # no return for now, no need
    with SessionManager() as session:
        session.bulk_save_objects(
            [TaskInstance.model_validate(ti) for ti in task_instances]
        )
        # print(f'{len(task_instances)} inserted')
        session.commit()


def get_last_successful_task_instance(task: Task) -> TaskInstance | None:
    """Get the last successful task instance for a task."""
    with SessionManager() as session:
        stmt = (
            select(TaskInstance)
            .where(
                and_(
                    TaskInstance.task_id == task.id,
                    TaskInstance.status == TaskInstanceStatus.OK.name,
                )
            )
            .order_by(TaskInstance.as_of.desc())
            .limit(1)
        )
        return session.exec(stmt).one_or_none()
