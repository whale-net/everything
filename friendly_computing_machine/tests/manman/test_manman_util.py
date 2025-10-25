from generated.py.manman.status_api.models import StatusType
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_enum import (
    Emoji,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.util import (
    get_emoji_from_status_type,
)


def test_get_emoji_from_status():
    # Test each status type
    for status_type in StatusType:
        emoji = get_emoji_from_status_type(status_type)
        assert isinstance(emoji, str), (
            f"Expected string for status {status_type}, got {emoji}"
        )

    # Test an unknown status type
    assert get_emoji_from_status_type("unknown_status") == Emoji.QUESTION
