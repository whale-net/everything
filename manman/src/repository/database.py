"""
Database repository for status and worker operations.

This module contains all database operations related to status tracking,
worker management, and related functionality extracted from the status processor.
"""

from datetime import datetime, timedelta, timezone
from typing import List, Optional, Tuple

from sqlalchemy import desc, exists
from sqlalchemy.orm import aliased
from sqlalchemy.sql.functions import current_timestamp
from sqlmodel import Session, not_, select

from libs.python.postgres import DatabaseRepository as BaseRepository
from manman.src.exceptions import (
    GameServerInstanceAlreadyClosedException,
    WorkerAlreadyClosedException,
)
from manman.src.models import (
    ACTIVE_STATUS_TYPES,
    ExternalStatusInfo,
    GameServer,
    GameServerConfig,
    GameServerInstance,
    StatusType,
    Worker,
)


class DatabaseRepository(BaseRepository):
    """Repository class for database operations related to status and worker management."""

    def __init__(self, session: Optional[Session] = None):
        """
        Initialize the repository with an optional session.

        Args:
            session: Optional SQLAlchemy session. If not provided, a new session will be created for each operation.
        """
        # Get session factory from util module
        from manman.src.util import get_sqlalchemy_session
        
        super().__init__(
            session=session,
            session_factory=get_sqlalchemy_session if session is None else None,
        )

    def _get_session(self) -> Session:
        """Get a database session. If none provided, create a new one."""
        if self._session is not None:
            return self._session

        # Import here to avoid circular import
        from manman.src.util import get_sqlalchemy_session

        return get_sqlalchemy_session()

    @staticmethod
    def get_worker_current_status_subquery():
        # Subquery to get the latest status for each worker
        inner = select(ExternalStatusInfo).alias("si2")
        last_status = (
            select(ExternalStatusInfo).where(
                not_(ExternalStatusInfo.worker_id.is_(None)),
                not_(
                    select(inner)
                    .where(
                        inner.c.worker_id == ExternalStatusInfo.worker_id,
                        inner.c.as_of > ExternalStatusInfo.as_of,
                    )
                    .exists()
                ),
            )
        ).subquery()
        return last_status

    def get_workers_by_heartbeat_status(
        self, heartbeat_threshold: datetime, heartbeat_max_lookback: datetime
    ) -> tuple[List[Tuple[Worker, str]], List[Tuple[Worker, str]]]:
        """
        Get workers whose heartbeats are stale or have recovered, along with their current status.

        Args:
            heartbeat_threshold: Workers with heartbeat before this time are considered stale
            heartbeat_max_lookback: Only consider workers that had a heartbeat within this lookback period

        Returns:
            Tuple of (stale_workers, recovered_workers) where each is a list of tuples containing (Worker, current_status_type)
            - stale_workers: Workers with active status but stale heartbeats
            - recovered_workers: Workers with LOST status but active heartbeats
        """
        with self._get_session_context() as session:
            last_status = DatabaseRepository.get_worker_current_status_subquery()

            # Base query shared by both stale and recovered workers
            base_query = (
                select(Worker, last_status.c.status_type)
                .join(last_status)
                .where(
                    Worker.end_date.is_(None),
                )
            )

            # Query for workers with stale heartbeats (active status but old heartbeat)
            stale_workers_query = base_query.where(
                Worker.last_heartbeat > heartbeat_max_lookback,
                Worker.last_heartbeat < heartbeat_threshold,
                last_status.c.status_type.in_(ACTIVE_STATUS_TYPES),
            )

            # Query for workers that have recovered (LOST status but recent heartbeat)
            recovered_workers_query = base_query.where(
                Worker.last_heartbeat >= heartbeat_threshold,
                last_status.c.status_type == StatusType.LOST,
            )

            return (
                session.exec(stale_workers_query).all(),
                session.exec(recovered_workers_query).all(),
            )

    def get_active_game_server_instances(
        self, worker_id: int
    ) -> List[GameServerInstance]:
        """
        Get all active game server instances for a given worker.

        Args:
            worker_id: The ID of the worker

        Returns:
            List of active GameServerInstance objects
        """
        with self._get_session_context() as session:
            stmt = (
                select(GameServerInstance)
                .where(GameServerInstance.worker_id == worker_id)
                .where(GameServerInstance.end_date.is_(None))
            )
            return session.exec(stmt).all()

    def write_external_status_to_database(
        self, status_info: ExternalStatusInfo
    ) -> None:
        """
        Write a status message to the database.

        Args:
            status_info: The StatusInfo object to write to the database
        """
        if status_info.status_info_id is not None and status_info.status_info_id < 0:
            status_info.status_info_id = None  # Ensure ID is None for new records
        with self._get_session_context() as session:
            session.add(status_info)
            session.commit()

    def get_workers_with_heartbeat_status(
        self,
        heartbeat_threshold_seconds: int = 5,
        heartbeat_max_lookback_hours: int = 1,
    ) -> tuple[List[Tuple[Worker, str]], List[Tuple[Worker, str]]]:
        """
        Convenience method to get workers with stale or recovered heartbeats using default time periods.

        Args:
            heartbeat_threshold_seconds: Seconds before now to consider heartbeat stale (default: 5)
            heartbeat_max_lookback_hours: Hours before now to look back for workers (default: 1)

        Returns:
            Tuple of (stale_workers, recovered_workers) where each is a list of tuples containing (Worker, current_status_type)
            - stale_workers: Workers with active status but stale heartbeats
            - recovered_workers: Workers with LOST status but active heartbeats
        """
        current_time = datetime.now(timezone.utc)
        heartbeat_threshold = current_time - timedelta(
            seconds=heartbeat_threshold_seconds
        )
        heartbeat_max_lookback = current_time - timedelta(
            hours=heartbeat_max_lookback_hours
        )

        return self.get_workers_by_heartbeat_status(
            heartbeat_threshold, heartbeat_max_lookback
        )


class StatusRepository(DatabaseRepository):
    """Repository class for status-related database operations."""

    def get_latest_worker_status(self, worker_id: int) -> Optional[ExternalStatusInfo]:
        """
        Get the latest status for a specific worker.

        Args:
            worker_id: The ID of the worker

        Returns:
            The latest StatusInfo for the worker, or None if not found
        """
        from opentelemetry import trace
        
        tracer = trace.get_tracer(__name__)
        
        with tracer.start_as_current_span(
            "status_repository.get_latest_worker_status",
            attributes={
                "db.operation": "select",
                "db.table": "external_status_info",
                "worker_id": worker_id,
            }
        ):
            with self._get_session_context() as session:
                stmt = (
                    select(ExternalStatusInfo)
                    .where(ExternalStatusInfo.worker_id == worker_id)
                    .order_by(desc(ExternalStatusInfo.as_of))
                    .limit(1)
                )
                result = session.exec(stmt).first()
                
                # Add result info to span
                current_span = trace.get_current_span()
                if current_span:
                    current_span.set_attribute("result.found", result is not None)
                    if result:
                        current_span.set_attribute("result.status_type", result.status_type)
                
                return result

    def get_prior_worker_status(self, worker_id: int) -> Optional[ExternalStatusInfo]:
        """
        Get the status before the current LOST status for a specific worker.
        This is useful for determining what status to restore when a worker recovers.

        Args:
            worker_id: The ID of the worker

        Returns:
            The prior StatusInfo before LOST, or None if not found or worker is not LOST
        """
        with self._get_session_context() as session:
            # Get the latest status
            latest = self.get_latest_worker_status(worker_id)
            if latest is None or latest.status_type != StatusType.LOST:
                return None

            # Get the status before the LOST status
            stmt = (
                select(ExternalStatusInfo)
                .where(ExternalStatusInfo.worker_id == worker_id)
                .where(ExternalStatusInfo.status_type != StatusType.LOST)
                .order_by(desc(ExternalStatusInfo.as_of))
                .limit(1)
            )
            return session.exec(stmt).first()

    def get_latest_instance_status(
        self, game_server_instance_id: int
    ) -> Optional[ExternalStatusInfo]:
        """
        Get the latest status for a specific game server instance.

        Args:
            game_server_instance_id: The ID of the game server instance

        Returns:
            The latest StatusInfo for the instance, or None if not found
        """
        with self._get_session_context() as session:
            stmt = (
                select(ExternalStatusInfo)
                .where(
                    ExternalStatusInfo.game_server_instance_id
                    == game_server_instance_id
                )
                .order_by(desc(ExternalStatusInfo.as_of))
                .limit(1)
            )
            return session.exec(stmt).first()

    def get_prior_instance_status(
        self, game_server_instance_id: int
    ) -> Optional[ExternalStatusInfo]:
        """
        Get the status before the current LOST status for a specific game server instance.
        This is useful for determining what status to restore when an instance recovers.

        Args:
            game_server_instance_id: The ID of the game server instance

        Returns:
            The prior StatusInfo before LOST, or None if not found or instance is not LOST
        """
        with self._get_session_context() as session:
            # Get the latest status
            latest = self.get_latest_instance_status(game_server_instance_id)
            if latest is None or latest.status_type != StatusType.LOST:
                return None

            # Get the status before the LOST status
            stmt = (
                select(ExternalStatusInfo)
                .where(
                    ExternalStatusInfo.game_server_instance_id
                    == game_server_instance_id
                )
                .where(ExternalStatusInfo.status_type != StatusType.LOST)
                .order_by(desc(ExternalStatusInfo.as_of))
                .limit(1)
            )
            return session.exec(stmt).first()


class WorkerRepository(DatabaseRepository):
    """Repository class for worker-related database operations."""

    def create_worker(self) -> Worker:
        """
        Create a new worker.

        Returns:
            The created Worker instance
        """
        with self._get_session_context() as session:
            worker = Worker()
            session.add(worker)
            session.flush()
            session.expunge(worker)
            session.commit()
            return worker

    def get_worker_by_id(self, worker_id: int) -> Optional[Worker]:
        """
        Get a worker by its ID.

        Args:
            worker_id: The ID of the worker

        Returns:
            The Worker instance, or None if not found
        """
        with self._get_session_context() as session:
            stmt = select(Worker).where(Worker.worker_id == worker_id)
            return session.exec(stmt).first()

    def shutdown_worker(self, worker_id: int) -> Optional[Worker]:
        """
        Shutdown a worker by setting its end_date.

        Args:
            worker_id: The ID of the worker to shutdown

        Returns:
            The updated Worker instance, or None if not found

        Raises:
            WorkerAlreadyClosedException: If the worker is already shut down
        """
        with self._get_session_context() as session:
            stmt = select(Worker).where(Worker.worker_id == worker_id)
            current_instance = session.exec(stmt).first()

            if current_instance is None:
                return None

            if current_instance.end_date is not None:
                raise WorkerAlreadyClosedException(
                    worker_id=worker_id, end_date=current_instance.end_date
                )

            current_instance.end_date = current_timestamp()
            session.add(current_instance)
            session.flush()
            session.refresh(current_instance)
            session.expunge(current_instance)
            session.commit()

            return current_instance

    def update_worker_heartbeat(self, worker_id: int) -> Optional[Worker]:
        """
        Update the heartbeat timestamp for a worker.

        Args:
            worker_id: The ID of the worker

        Returns:
            The updated Worker instance, or None if not found

        Raises:
            Exception: If the worker is not found
            WorkerAlreadyClosedException: If the worker is already shut down
        """

        with self._get_session_context() as session:
            stmt = select(Worker).where(Worker.worker_id == worker_id)
            current_instance = session.exec(stmt).first()

            if current_instance is None:
                raise Exception("Worker not found")

            if current_instance.end_date is not None:
                raise WorkerAlreadyClosedException(
                    worker_id=worker_id, end_date=current_instance.end_date
                )

            current_instance.last_heartbeat = current_timestamp()
            session.add(current_instance)
            session.flush()
            session.refresh(current_instance)
            session.expunge(current_instance)
            session.commit()

            return current_instance

    def close_other_workers(self, worker_id: int) -> list[Worker]:
        """
        Close all other workers except the specified one.

        Args:
            worker_id: The ID of the worker to keep active

        Returns:
            list of lost workers who are now terminated
        """
        from sqlmodel import and_, func, update

        with self._get_session_context() as session:
            # hopefully this is all one transaction
            # otherwise this doesn't work
            open_worker_condition = and_(
                Worker.worker_id != worker_id,
                Worker.end_date.is_(None),
            )

            last_status = DatabaseRepository.get_worker_current_status_subquery()
            lost_workers = session.exec(
                select(Worker)
                .join(last_status)
                .where(
                    open_worker_condition, last_status.c.status_type == StatusType.LOST
                )
            ).all()
            for worker in lost_workers:
                session.expunge(worker)

            update_stmt = (
                update(Worker).where(open_worker_condition).values(end_date=func.now())
            )
            # result = session.exec(update_stmt)
            # affected_rows = result.rowcount
            session.exec(update_stmt)
            session.commit()
            return lost_workers

    # TODO - this whole thing needs rethnking ,but just going to hack it together for now
    def get_current_worker(self) -> Worker:
        with self._get_session_context() as session:
            stmt = (
                select(Worker)
                .where(Worker.end_date.is_(None))
                .order_by(desc(Worker.created_date))
                .limit(1)
            )
            worker = session.exec(stmt).one_or_none()
            return worker


class GameServerRepository(DatabaseRepository):
    """Repository class for game server-related database operations."""

    def get_game_server_by_id(self, server_id: int) -> Optional[GameServer]:
        """
        Get a game server by its ID.

        Args:
            server_id: The ID of the game server

        Returns:
            The GameServer instance, or None if not found
        """
        with self._get_session_context() as session:
            server = session.get(GameServer, server_id)
            if server:
                session.expunge(server)
            return server

    def get_game_server_config_by_id(
        self, config_id: int
    ) -> Optional[GameServerConfig]:
        """
        Get a game server configuration by its ID.

        Args:
            config_id: The ID of the game server configuration

        Returns:
            The GameServerConfig instance, or None if not found
        """
        with self._get_session_context() as session:
            config = session.get(GameServerConfig, config_id)
            if config:
                session.expunge(config)
            return config


class GameServerConfigRepository(DatabaseRepository):
    """Repository class for game server configuration-related database operations."""

    def get_game_server_configs(self) -> list[GameServerConfig]:
        """
        Get all game server configurations.

        Returns:
            List of GameServerConfig instances
        """
        # TODO - is_visible parameter
        with self._get_session_context() as session:
            stmt = (
                select(GameServerConfig)
                .where(GameServerConfig.is_visible.is_(True))
                .order_by(GameServerConfig.name)
            )
            results = session.exec(stmt).all()
            for config in results:
                session.expunge(config)
            return results


class GameServerInstanceRepository(DatabaseRepository):
    """Repository class for game server instance-related database operations."""

    def create_instance(
        self, game_server_config_id: int, worker_id: int
    ) -> GameServerInstance:
        """
        Create a new game server instance.

        Args:
            game_server_config_id: The ID of the game server configuration
            worker_id: The ID of the worker

        Returns:
            The created GameServerInstance
        """
        with self._get_session_context() as session:
            server = GameServerInstance(
                game_server_config_id=game_server_config_id, worker_id=worker_id
            )
            session.add(server)
            session.flush()
            session.expunge(server)
            session.commit()
            return server

    def get_instance_by_id(self, instance_id: int) -> Optional[GameServerInstance]:
        """
        Get a game server instance by its ID.

        Args:
            instance_id: The ID of the game server instance

        Returns:
            The GameServerInstance, or None if not found
        """
        with self._get_session_context() as session:
            instance = session.get(GameServerInstance, instance_id)
            if instance:
                session.expunge(instance)
            return instance

    def shutdown_instance(self, instance_id: int) -> Optional[GameServerInstance]:
        """
        Shutdown a game server instance by setting its end_date.

        Args:
            instance_id: The ID of the instance to shutdown

        Returns:
            The updated GameServerInstance, or None if not found

        Raises:
            GameServerInstanceAlreadyClosedException: If the instance is already shut down
        """
        with self._get_session_context() as session:
            stmt = select(GameServerInstance).where(
                GameServerInstance.game_server_instance_id == instance_id
            )
            current_instance = session.exec(stmt).first()

            if current_instance is None:
                return None

            if current_instance.end_date is not None:
                raise GameServerInstanceAlreadyClosedException(
                    instance_id=instance_id, end_date=current_instance.end_date
                )

            current_instance.end_date = datetime.now(timezone.utc)
            session.add(current_instance)
            session.flush()
            session.refresh(current_instance)
            session.expunge(current_instance)
            session.commit()

            return current_instance

    def update_instance_heartbeat(
        self, instance_id: int
    ) -> Optional[GameServerInstance]:
        """
        Update the heartbeat timestamp for a game server instance.

        Args:
            instance_id: The ID of the instance

        Returns:
            The updated GameServerInstance, or None if not found
        """
        with self._get_session_context() as session:
            instance = session.get(GameServerInstance, instance_id)
            if instance is None:
                return None

            instance.last_heartbeat = datetime.now(timezone.utc)
            session.add(instance)
            session.flush()
            session.expunge(instance)
            session.commit()
            return instance

    def get_current_instances(
        self, worker_id: int, include_crashed: bool = False, session: Optional[Session] = None
    ) -> list[GameServerInstance]:
        """
        Get current game server instances for a worker.
        
        Args:
            worker_id: The worker ID to filter by
            include_crashed: If True, includes the last crashed instance for each game server config
            session: Optional database session
            
        Returns:
            List of active instances, and optionally the last crashed instance per config
        """
        # TODO - don't re-use a session in the context manager if one is provided
        #        doing so will cause the session to be closed when the context manager exits
        #        #35
        # TODO - addres if above todo is stil lrelevant - copy patsed from somewhere else in the project
        with self._get_session_context() as session:
            # Get active instances (end_date is NULL)
            active_stmt = (
                select(GameServerInstance)
                .where(GameServerInstance.worker_id == worker_id)
                .where(GameServerInstance.end_date.is_(None))
            )
            active_instances = list(session.exec(active_stmt).all())
            
            if not include_crashed:
                return active_instances
            
            # Get the last crashed instance for each game_server_config_id
            # that doesn't already have an active instance
            active_config_ids = {inst.game_server_config_id for inst in active_instances}
            
            if active_config_ids:
                # Use NOT IN clause to exclude configs with active instances
                config_filter = GameServerInstance.game_server_config_id.notin_(active_config_ids)
            else:
                # No active instances, so include all configs
                config_filter = True
            
            # Aliases for the subquery to avoid column name conflicts
            GSI2 = aliased(GameServerInstance)
            ESI2 = aliased(ExternalStatusInfo)
            
            # Get crashed instances where there does not exist a crashed instance
            # with a later end_date for the same config
            crashed_stmt = (
                select(GameServerInstance)
                .join(
                    ExternalStatusInfo,
                    ExternalStatusInfo.game_server_instance_id == GameServerInstance.game_server_instance_id
                )
                .where(GameServerInstance.worker_id == worker_id)
                .where(GameServerInstance.end_date.isnot(None))
                .where(ExternalStatusInfo.status_type == StatusType.CRASHED)
                .where(config_filter)
                .where(
                    ~exists(
                        select(GSI2.game_server_instance_id)
                        .join(
                            ESI2,
                            ESI2.game_server_instance_id == GSI2.game_server_instance_id
                        )
                        .where(GSI2.worker_id == worker_id)
                        .where(GSI2.end_date.isnot(None))
                        .where(ESI2.status_type == StatusType.CRASHED)
                        .where(GSI2.game_server_config_id == GameServerInstance.game_server_config_id)
                        .where(GSI2.end_date > GameServerInstance.end_date)
                    )
                )
            )
            
            crashed_instances = list(session.exec(crashed_stmt).all())
            
            return active_instances + crashed_instances
