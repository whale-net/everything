import datetime

from sqlmodel import Field, Index, UniqueConstraint

from generated.py.manman.status_api.models.external_status_info import ExternalStatusInfo
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base


class ManManStatusUpdateBase(Base):
    """
    Represents a status update for ManMan.
    """

    current_status: str
    # "server" or "worker"
    service_type: str
    # the primary key id
    # could be string for future proof but we'll get there when we get there
    service_id: int

    as_of: datetime.datetime


class ManManStatusUpdate(ManManStatusUpdateBase, table=True):
    """
    Represents a status update for ManMan, stored in the database.
    """

    __table_args__ = (
        UniqueConstraint("service_id", "service_type"),
        # service_id first because it has higher cardinality
        Index("idx_service", "service_id", "service_type"),
        Index("idx_as_of_service", "as_of", "service_id", "service_type"),
    )

    id: int = Field(default=None, nullable=False, primary_key=True)

    # trying out putting this on the model instead of the base class
    slack_message_id: int | None = Field(
        nullable=True,
        index=True,
        foreign_key="slackmessage.id",
        default=None,
    )


class ManManStatusUpdateCreate(ManManStatusUpdateBase):
    @classmethod
    def from_status_info(
        cls, status_info: ExternalStatusInfo
    ) -> "ManManStatusUpdateCreate":
        """
        Create a ManManStatusUpdateCreate instance from a ExternalStatusInfo instance.
        """

        service_type = (
            "server" if status_info.game_server_instance_id is not None else "worker"
        )
        service_id = status_info.game_server_instance_id or status_info.worker_id

        return cls(
            current_status=status_info.status_type.value,
            service_type=service_type,
            service_id=service_id,
            as_of=status_info.as_of,
            # not passed in from status info
        )
