from generated.manman.status_api.models import StatusType
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_enum import (
    Emoji,
)

INACTIVE_STATUSES = {
    StatusType.CREATED,
    StatusType.INITIALIZING,
    StatusType.LOST,
}

ACTIVE_STATUSES = {
    StatusType.RUNNING,
}

TERMINATED_STATUSES = {
    StatusType.COMPLETE,
    StatusType.CRASHED,
}


def get_emoji_from_status_type(status: StatusType) -> str:
    """
    Get the emoji representation for a given status.

    Args:
        status (StatusType): The status type.

    Returns:
        str: The emoji string corresponding to the status.
    """
    if status == StatusType.CREATED:
        return Emoji.MEGNO
    elif status == StatusType.INITIALIZING:
        return Emoji.SPINNING_GNOME
    elif status == StatusType.RUNNING:
        return Emoji.GNOME_COOL
    elif status == StatusType.LOST:
        return Emoji.GNOME_SHAKE
    elif status == StatusType.COMPLETE:
        return Emoji.RAIN_GNOME
    elif status == StatusType.CRASHED:
        return Emoji.ZOOM_GNOME
    else:
        return Emoji.QUESTION
