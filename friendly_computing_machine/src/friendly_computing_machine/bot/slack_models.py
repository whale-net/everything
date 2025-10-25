"""Slack block models using slack_bolt/slack_sdk components."""

import logging
import uuid
from typing import List

from slack_sdk.models.blocks import (
    ActionsBlock,
    Block,
    ButtonElement,
    CallBlock,
    ContextBlock,
    DividerBlock,
    FileBlock,
    HeaderBlock,
    ImageBlock,
    InputBlock,
    PlainTextObject,
    RichTextBlock,
    SectionBlock,
    VideoBlock,
)

from generated.py.manman.status_api.models.external_status_info import ExternalStatusInfo
from generated.py.manman.status_api.models.status_type import StatusType
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_enum import (
    Emoji,
    SlackActionRegistry,
)
from friendly_computing_machine.src.friendly_computing_machine.manman.util import (
    ACTIVE_STATUSES,
    get_emoji_from_status_type,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackSpecialChannelType,
)

logger = logging.getLogger(__name__)


def nonce() -> str:
    """Generate a random nonce string."""
    return str(uuid.uuid4().hex)


# TODO - unit test for each block
def render_blocks_to_text(blocks: List) -> str:
    """
    Render a list of Slack blocks to plain text for database storage.

    Args:
        blocks: List of Slack block objects

    Returns:
        Plain text representation of the blocks
    """
    text_parts = []

    for block in blocks:
        if isinstance(block, SectionBlock):
            # Extract text from SectionBlock
            text_obj = block.text
            if isinstance(text_obj, dict):
                text_parts.append(text_obj.get("text", ""))
            elif hasattr(text_obj, "text"):
                text_parts.append(text_obj.text)
        elif isinstance(block, DividerBlock):
            # Represent divider as a line separator
            text_parts.append("---")
        elif isinstance(block, ActionsBlock):
            # Extract button text from ActionsBlock
            button_texts = []
            for element in block.elements:
                if isinstance(element, ButtonElement):
                    if hasattr(element.text, "text"):
                        button_texts.append(f"[{element.text.text}]")
                    elif isinstance(element.text, dict):
                        button_texts.append(f"[{element.text.get('text', '')}]")
            if button_texts:
                text_parts.append(" ".join(button_texts))
        elif isinstance(block, HeaderBlock):
            # Extract header text
            if hasattr(block.text, "text"):
                text_parts.append(f"# {block.text.text}")
            elif isinstance(block.text, dict):
                text_parts.append(f"# {block.text.get('text', '')}")
        elif isinstance(block, ContextBlock):
            # Extract context elements text
            context_texts = []
            for element in block.elements:
                if hasattr(element, "text"):
                    context_texts.append(element.text)
                elif isinstance(element, dict):
                    context_texts.append(element.get("text", ""))
            if context_texts:
                text_parts.append(f"({' | '.join(context_texts)})")
        elif isinstance(block, ImageBlock):
            # Represent image with alt text or URL
            alt_text = block.alt_text if hasattr(block, "alt_text") else "Image"
            text_parts.append(f"[{alt_text}]")
        elif isinstance(block, FileBlock):
            # Represent file block
            text_parts.append("[File]")
        elif isinstance(block, InputBlock):
            # Extract label text
            if hasattr(block.label, "text"):
                text_parts.append(f"Input: {block.label.text}")
            elif isinstance(block.label, dict):
                text_parts.append(f"Input: {block.label.get('text', '')}")
        elif isinstance(block, VideoBlock):
            # Represent video with title or alt text
            title = block.title_url if hasattr(block, "title_url") else "Video"
            text_parts.append(f"[{title}]")
        elif isinstance(block, RichTextBlock):
            # Extract text from rich text elements
            rich_texts = []
            if hasattr(block, "elements"):
                for element in block.elements:
                    if hasattr(element, "elements"):
                        for sub_element in element.elements:
                            if hasattr(sub_element, "text"):
                                rich_texts.append(sub_element.text)
            if rich_texts:
                text_parts.append(" ".join(rich_texts))
        elif isinstance(block, CallBlock):
            # Represent call block
            text_parts.append("[Call]")

    return "\n".join(filter(None, text_parts))


def create_manman_status_blocks(
    special_channel_type: SlackSpecialChannelType, current_status: ExternalStatusInfo
) -> list[Block]:
    if current_status.worker_id is not None:
        return create_worker_status_blocks(special_channel_type, current_status)
    elif current_status.game_server_instance_id is not None:
        return create_server_status_blocks(special_channel_type, current_status)
    else:
        raise ValueError(
            f"Unsupported ManMan service type: {current_status.class_name}"
        )


def create_server_status_blocks(
    special_chan: SlackSpecialChannelType, current_status: ExternalStatusInfo
) -> list[Block]:
    """
    Create a block payload for server status display.
    Args:
        special_chan: The special channel type containing the friendly name
        current_status: The current status of the server
    Returns:
        List of Slack blocks representing the server status message
    """
    should_include_buttons = current_status.status_type == StatusType.RUNNING
    buttons_control_group = []
    buttons_server_control_group = []
    buttons_server_mutate_group = []
    if should_include_buttons:
        buttons_control_group.append(
            ButtonElement(
                text=PlainTextObject(text="Stop"),
                style="danger",
                # value=str(current_status.game_server_instance_id),
                # TEMP to server
                value="1",
                action_id=SlackActionRegistry.MANMAN_SERVER_STOP,
            )
        )
        # Add a bunch of arbitrary buttons for testing purposes
        # these are specific to the manman server
        CS_MAPS = [
            ("de_train", "train"),
            ("de_ancient", "ancient"),
            ("de_anubis", "anubis"),
            ("de_dust2", "dust2"),
            ("de_inferno", "inferno"),
            ("de_mirage", "mirage"),
            ("de_nuke", "nuke"),
            ("de_overpass", "overpass"),
            ("cs_office", "office"),
            ("de_vertigo", "vertigo"),
            ("de_basalt", "basalt"),
            ("de_edin", "edin"),
            ("cs_italy", "italy"),
        ]
        for map_name, map_value in CS_MAPS:
            buttons_server_control_group.append(
                ButtonElement(
                    text=PlainTextObject(text=map_value),
                    value=f"1;changelevel {map_name}",
                    action_id=f"{SlackActionRegistry.MANMAN_SERVER_STDIN}_{nonce()}",
                )
            )
        # buytime custom?
        buttons_server_mutate_group.append(
            ButtonElement(
                text=PlainTextObject(text="mp_restartgame"),
                value="1;mp_restartgame 1",
                action_id=f"{SlackActionRegistry.MANMAN_SERVER_STDIN}_{nonce()}",
            )
        )

        buttons_server_mutate_group.append(
            ButtonElement(
                text=PlainTextObject(text="sv_cheats"),
                value="1;incrementvar sv_cheats 0 1 1; say sv_cheats toggled",
                action_id=f"{SlackActionRegistry.MANMAN_SERVER_STDIN}_{nonce()}",
            )
        )
        buttons_server_mutate_group.append(
            ButtonElement(
                text=PlainTextObject(text="mp_autoteambalance"),
                value="1;incrementvar mp_autoteambalance 0 1 1; say mp_autoteambalance",
                action_id=f"{SlackActionRegistry.MANMAN_SERVER_STDIN}_{nonce()}",
            )
        )
        buttons_server_mutate_group.append(
            ButtonElement(
                text=PlainTextObject(text="mp_limitteams"),
                value="1;incrementvar mp_limitteams  0 1 1; say mp_limitteams",
                action_id=f"{SlackActionRegistry.MANMAN_SERVER_STDIN}_{nonce()}",
            )
        )

    logger.debug("these are muh instance control buttons: %s", buttons_control_group)
    logger.debug(
        "these are muh server control buttons: %s", buttons_server_control_group
    )
    logger.debug("these are muh server mutate buttons: %s", buttons_server_mutate_group)

    # justin picked this. I repeat, I did not pick this.
    server_emoji = Emoji.CHICKEN_JOCKEY
    status_emoji = get_emoji_from_status_type(current_status.status_type)
    base_status_text = (
        f"{server_emoji} {special_chan.friendly_type_name} "
        f"- {current_status.class_name} ({current_status.game_server_instance_id}) "
        f"{current_status.status_type.value} {status_emoji}"
    )

    blocks = [
        SectionBlock(text={"type": "mrkdwn", "text": base_status_text}),
    ]

    if should_include_buttons and any(
        (
            buttons_server_mutate_group,
            buttons_server_control_group,
            buttons_control_group,
        )
    ):
        blocks.append(DividerBlock())
        if buttons_control_group:
            blocks.append(ActionsBlock(elements=buttons_control_group))
        if buttons_server_control_group:
            blocks.append(ActionsBlock(elements=buttons_server_control_group))
        if buttons_server_mutate_group:
            blocks.append(ActionsBlock(elements=buttons_server_mutate_group))

    return blocks


def create_worker_status_blocks(
    special_chan: SlackSpecialChannelType, current_status: ExternalStatusInfo
) -> list[Block]:
    """
    Create a block payload for worker status display.

    Args:
        friendly_name: The friendly display name of the worker
        name: The worker name
        id: The worker ID
        current_status: The current status of the worker

    Returns:
        List of Slack blocks representing the worker status message
    """

    # buttons.append(
    #     ButtonElement(
    #         text=PlainTextObject(text="Stop TestCS2", emoji=True),
    #         style="danger",
    #         value="1",
    #         action_id=SlackActionRegistry.MANMAN_SERVER_STOP,
    #     )
    # )

    # note: list, order matters
    block_list = []

    worker_emoji = Emoji.SATELLITE_ANTENNA
    base_block_base = f"{worker_emoji} {special_chan.friendly_type_name} worker ({current_status.worker_id})"
    block_list.append(
        SectionBlock(
            text={
                "type": "mrkdwn",
                "text": f"{base_block_base} - {current_status.status_type.value} {get_emoji_from_status_type(current_status.status_type)}",
            }
        )
    )

    should_include_buttons = current_status.status_type in ACTIVE_STATUSES
    if should_include_buttons:
        buttons = [
            # TODO - confirmation dialog for stop
            ButtonElement(
                text=PlainTextObject(text="Stop", emoji=True),
                style="danger",
                value=str(current_status.worker_id),
                action_id=SlackActionRegistry.MANMAN_WORKER_STOP,
            ),
            # Arbitrary button for testing purposes - should be dynamic in the future
            ButtonElement(
                text=PlainTextObject(text="Start TestCS2", emoji=True),
                value="1",
                action_id=SlackActionRegistry.MANMAN_SERVER_CREATE,
            ),
        ]
        block_list.append(DividerBlock())
        block_list.append(ActionsBlock(elements=buttons))

    return block_list
