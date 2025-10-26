import datetime
from enum import Enum, StrEnum
from typing import Any, Optional

from pydantic import model_serializer
from sqlalchemy import (  # using postgres.ARRAY I guess; MetaData,; ForeignKey,
    ARRAY,
    CheckConstraint,  # Add CheckConstraint
    Column,
    Index,
    String,
)

# , UniqueConstraint
from sqlalchemy.sql.functions import current_timestamp

# from sqlalchemy.dialects import postgresql
from sqlmodel import Field, MetaData, Relationship, SQLModel

from manman.src.constants import EntityRegistry

# SQLModel.metadata = MetaData(schema="manman")


class ServerType(Enum):
    STEAM = 1


class ManManBase(SQLModel):
    metadata = MetaData(schema="manman")
    
    @model_serializer(mode='wrap')
    def _serialize_model(self, serializer: Any) -> dict[str, Any]:
        """Custom serializer to ensure datetime fields have timezone info (RFC3339 compatible)."""
        data = serializer(self)
        
        # Convert any datetime values to RFC3339 format with timezone
        for key, value in data.items():
            if isinstance(value, datetime.datetime):
                if value.tzinfo is None:
                    value = value.replace(tzinfo=datetime.timezone.utc)
                data[key] = value.isoformat()
        
        return data


class Worker(ManManBase, table=True):
    __tablename__ = "workers"
    worker_id: int = Field(primary_key=True)
    # ip_addr: Mapped[str] = mapped_column(String(15))
    created_date: datetime.datetime = Field(default=current_timestamp())
    end_date: Optional[datetime.datetime] = Field(nullable=True)

    last_heartbeat: Optional[datetime.datetime] = Field(nullable=True)
    # TODO FIGURE THIS OUT
    # game_server_instances: Mapped["GameServerInstance"] = relationship(

    game_server_instances: list["GameServerInstance"] = Relationship(
        back_populates="worker"
        # TODO - lazy? make everything lazy? probably yes, in most situations I'd think
    )


# do I need server table? -> yes, but make worker manage state
# make health check contain server info for trueups
class GameServerInstance(ManManBase, table=True):
    __tablename__ = "game_server_instances"
    game_server_instance_id: int = Field(primary_key=True)

    game_server_config_id: int = Field(
        foreign_key="game_server_configs.game_server_config_id", index=True
    )

    game_server_config: "GameServerConfig" = Relationship()

    created_date: datetime.datetime = Field(default=current_timestamp())
    end_date: Optional[datetime.datetime] = Field(nullable=True)

    # should not be nullable, but for now it is
    worker_id: int = Field(foreign_key="workers.worker_id", index=True, nullable=True)
    worker: Optional[Worker] = Relationship(back_populates="game_server_instances")

    last_heartbeat: Optional[datetime.datetime] = Field(nullable=True)

    def get_thread_name(self, extra: Optional[str] = None) -> str:
        extra_str = "-" + extra if extra is not None else ""
        return f"server[{self.game_server_instance_id}{extra_str}]"


class GameServer(ManManBase, table=True):
    __tablename__ = "game_servers"
    game_server_id: int = Field(primary_key=True)
    name: str = Field()
    server_type: ServerType = Field()
    # for now only steam, everything is app_id
    # in the future, not sure what will be supported, so for now going to ignore it
    app_id: int = Field()

    __table_args__ = (
        # this should really be on app_id
        Index(
            "ixu_game_server_name_server_type",
            "name",
            "server_type",
            unique=True,
        ),
        Index(
            "ixu_game_server_app_id_server_type",
            "app_id",
            "server_type",
            unique=True,
        ),
    )


class GameServerConfig(ManManBase, table=True):
    __tablename__ = "game_server_configs"
    game_server_config_id: int = Field(primary_key=True)
    game_server_id: int = Field(foreign_key="game_servers.game_server_id", index=True)
    is_default: bool = Field(default=False)
    is_visible: bool = Field(default=True)

    name: str = Field()
    executable: str = Field()
    args: list[str] = Field(sa_column=Column(ARRAY(String()), nullable=False))
    env_var: list[str] = Field(sa_column=Column(ARRAY(String()), nullable=False))

    __table_args__ = (
        Index(
            "ixuf_game_server_configs_default_config",
            "game_server_id",
            "is_default",
            unique=True,
            postgresql_where=("is_default"),
        ),
        Index(
            "ixu_game_server_configs_game_server_id_name",
            "game_server_id",
            "name",
            unique=True,
        ),
    )


### NON TABLES # unsure if this is good idea


class CommandType(Enum):
    START = "START"
    STDIN = "STDIN"
    STOP = "STOP"


# {"command_type":"START", "command_args": ["1"]}
# {"command_type":"START", "command_args": ["3"]}
# {"command_type":"START", "command_args": []}
# {"command_type":"STOP", "command_args": []}
# TODO subclass for each comamnd type + parent class factory based on enum
class Command(ManManBase):
    command_type: CommandType = Field()
    command_args: list[str] = Field(default=[])


# See https://github.com/whale-net/friendly-computing-machine/blob/main/docs/manman_subscribe.md
class StatusType(Enum):
    CREATED = "CREATED"
    # Optional, can go from CREATED -> RUNNING
    INITIALIZING = "INITIALIZING"
    RUNNING = "RUNNING"
    LOST = "LOST"
    COMPLETE = "COMPLETE"
    CRASHED = "CRASHED"


ACTIVE_STATUS_TYPES = {
    StatusType.CREATED,
    StatusType.INITIALIZING,
    StatusType.RUNNING,
}
# Although a little strange, these are types that cannot be produced by a running system
# and must be observed by the status processor
OBSERVED_STATUS_TYPES = {
    StatusType.LOST,
    StatusType.CRASHED,
}


# TODO can there be a common message base?
class InternalStatusInfo(ManManBase, table=False):
    def __init__(self, **data):
        # TODO improve this 6/8/25

        if "entity_type" in data:
            entity_type = data["entity_type"]
            if isinstance(entity_type, str):
                try:
                    data["entity_type"] = EntityRegistry(entity_type)
                except ValueError:
                    raise ValueError(
                        f"Invalid entity type: {entity_type}. Must be a valid EntityRegistrar."
                    )
            elif not isinstance(entity_type, EntityRegistry):
                raise ValueError(f"Invalid entity type: {entity_type}")
        if "status_type" in data:
            status_type = data["status_type"]
            if isinstance(status_type, str):
                try:
                    data["status_type"] = StatusType(status_type)
                except ValueError:
                    raise ValueError(
                        f"Invalid status type: {status_type}. Must be a valid StatusType."
                    )
            elif not isinstance(status_type, StatusType):
                raise ValueError(f"Invalid status type: {status_type}")
        super().__init__(**data)

    entity_type: EntityRegistry = Field()
    identifier: str = Field()
    as_of: datetime.datetime = Field(default=current_timestamp())
    status_type: StatusType = Field()

    @classmethod
    def create(
        cls,
        entity_type: EntityRegistry,
        identifier: str,
        status_type: StatusType,
    ) -> "InternalStatusInfo":
        as_of = datetime.datetime.now(datetime.timezone.utc)
        return cls(
            entity_type=entity_type,
            identifier=identifier,
            status_type=status_type,
            as_of=as_of,
        )


### BACK TO TABLES


class ExternalStatusInfo(ManManBase, table=True):
    __tablename__ = "status_info"

    def __init__(self, **data):
        """
        Custom init to handle status_type validation.
        This is necessary because Pydantic's field_validator
        is not called during the __init__ method for whatever reason
        when table=True is set (or some other sqlmodel behavior)

        This is frustrating, but can be worked around by doing the validation here.
        Ideally, we do what fcm does with models, but that is a lot of work
        and not sure if worth it. For now, this is a good enough solution.
        """
        # Handle status_type validation before calling super().__init__
        if "status_type" in data:
            status_type = data["status_type"]
            if isinstance(status_type, str):
                try:
                    data["status_type"] = StatusType(status_type)
                except ValueError:
                    raise ValueError(
                        f"Invalid status type: {status_type}. Must be a valid StatusType."
                    )
            elif not isinstance(status_type, StatusType):
                raise ValueError(f"Invalid status type: {status_type}")

        super().__init__(**data)

        # validate target exclusivity after init
        # This is necessary because Pydantic's model_validator is not called
        # for whatever reason
        self._validate_target_exclusivity(self)

    class_name: str = Field()
    status_type: StatusType = Field()
    as_of: datetime.datetime = Field(default=current_timestamp())

    status_info_id: int = Field(primary_key=True)

    worker_id: Optional[int] = Field(  # Made Optional
        default=None, foreign_key="workers.worker_id", index=True
    )
    worker: Optional[Worker] = Relationship()  # Made Optional

    game_server_instance_id: Optional[int] = Field(  # Made Optional
        default=None,
        foreign_key="game_server_instances.game_server_instance_id",
        index=True,
    )
    game_server_instance: Optional[GameServerInstance] = Relationship()  # Made Optional

    __table_args__ = (
        CheckConstraint(
            "(worker_id IS NULL AND game_server_instance_id IS NOT NULL) OR "
            "(worker_id IS NOT NULL AND game_server_instance_id IS NULL)",
            name="chk_status_info_target_not_null",
        ),
        Index(
            "ix_status_info_as_of_game_server_instance_worker",
            "as_of",
            "game_server_instance_id",
            "worker_id",
        ),
        # Preserve existing indexes if any, or add them here if needed
        # e.g., Index("ix_status_info_worker_id", "worker_id"),
        # Index("ix_status_info_game_server_instance_id", "game_server_instance_id"),
    )

    @staticmethod
    def _validate_target_exclusivity(
        info: "ExternalStatusInfo",
    ) -> "ExternalStatusInfo":
        """Ensure exactly one of worker_id or game_server_instance_id is set."""
        worker_set = info.worker_id is not None
        instance_set = info.game_server_instance_id is not None

        if worker_set and instance_set:
            raise ValueError("Cannot set both worker_id and game_server_instance_id")

        if not worker_set and not instance_set:
            raise ValueError("Must set either worker_id or game_server_instance_id")

        return info

    @classmethod
    def create_from_internal(
        cls,
        internal_status: InternalStatusInfo,
    ) -> "ExternalStatusInfo":
        """
        Create an ExternalStatusInfo from an InternalStatusInfo.
        This is useful for converting internal status messages to external ones.
        """

        if internal_status.entity_type == EntityRegistry.WORKER:
            worker_id = int(internal_status.identifier)
            game_server_instance_id = None
        elif internal_status.entity_type == EntityRegistry.GAME_SERVER_INSTANCE:
            worker_id = None
            game_server_instance_id = int(internal_status.identifier)
        else:
            raise ValueError(
                f"Invalid entity type: {internal_status.entity_type}. Must be either WORKER or GAME_SERVER_INSTANCE."
            )

        return cls.create(
            class_name=str(internal_status.entity_type),
            status_type=internal_status.status_type,
            worker_id=worker_id,
            game_server_instance_id=game_server_instance_id,
            # pass through the as_of timestamp
            _as_of=internal_status.as_of,
        )

    @classmethod
    def create(
        cls,
        class_name: str,
        status_type: StatusType,
        worker_id: Optional[int] = None,
        game_server_instance_id: Optional[int] = None,
        _as_of: Optional[datetime.datetime] = None,
    ) -> "ExternalStatusInfo":
        as_of = _as_of or datetime.datetime.now(datetime.timezone.utc)
        if isinstance(status_type, str):
            raise ValueError(
                f"Invalid status type: {status_type}. Must be an instance of StatusType."
            )
        return cls(
            # -1 is placeholder
            status_info_id=-1,
            class_name=class_name,
            status_type=status_type,
            as_of=as_of,
            worker_id=worker_id,
            game_server_instance_id=game_server_instance_id,
        )
