"""Slack block models using slack_bolt/slack_sdk components."""

from typing import List

from slack_sdk.models.blocks import (
    ActionsBlock,
    ButtonElement,
    CallBlock,
    ContextBlock,
    DividerBlock,
    FileBlock,
    HeaderBlock,
    ImageBlock,
    InputBlock,
    RichTextBlock,
    SectionBlock,
    VideoBlock,
)


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


