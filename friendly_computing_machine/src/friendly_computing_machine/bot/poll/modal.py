"""Poll-creation modal opened by a bare `/wpoll`."""

import json
from dataclasses import dataclass

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    MAX_OPTION_LEN,
    MAX_OPTIONS,
    MAX_QUESTION_LEN,
    MIN_OPTIONS,
    PollParseError,
    PollSpec,
    build_poll_spec,
)

POLL_MODAL_CALLBACK = "poll_create_modal"
POLL_ADD_OPTION_ACTION = "poll_add_option"

INITIAL_OPTION_FIELDS = 3
_VALUE = "value"
QUESTION_BLOCK = "poll_question"
OPTION_BLOCK_PREFIX = "poll_option_"
SETTINGS_BLOCK = "poll_settings"
LIMIT_BLOCK = "poll_limit"
_ANONYMOUS = "anonymous"
_UNLIMITED = "unlimited"


@dataclass(frozen=True)
class PollModalMetadata:
    channel_id: str
    option_fields: int = INITIAL_OPTION_FIELDS

    def dumps(self) -> str:
        return json.dumps(
            {"channel_id": self.channel_id, "option_fields": self.option_fields}
        )

    @classmethod
    def loads(cls, raw: str) -> "PollModalMetadata":
        data = json.loads(raw)
        return cls(channel_id=data["channel_id"], option_fields=data["option_fields"])


def _plain(text: str) -> dict:
    return {"type": "plain_text", "text": text}


def _option_block(idx: int) -> dict:
    return {
        "type": "input",
        "block_id": f"{OPTION_BLOCK_PREFIX}{idx}",
        # only the minimum number of options is required
        "optional": idx >= MIN_OPTIONS,
        "label": _plain(f"Option {idx + 1}"),
        "element": {
            "type": "plain_text_input",
            "action_id": _VALUE,
            "max_length": MAX_OPTION_LEN,
            "placeholder": _plain("Add an option"),
        },
    }


def build_poll_modal(metadata: PollModalMetadata) -> dict:
    blocks: list[dict] = [
        {
            "type": "input",
            "block_id": QUESTION_BLOCK,
            "label": _plain("Question"),
            "element": {
                "type": "plain_text_input",
                "action_id": _VALUE,
                "max_length": MAX_QUESTION_LEN,
                "placeholder": _plain("What do you want to ask?"),
            },
        }
    ]
    blocks.extend(_option_block(i) for i in range(metadata.option_fields))
    if metadata.option_fields < MAX_OPTIONS:
        blocks.append(
            {
                "type": "actions",
                "elements": [
                    {
                        "type": "button",
                        "action_id": POLL_ADD_OPTION_ACTION,
                        "text": _plain("+ Add another option"),
                    }
                ],
            }
        )

    limit_choices = [{"text": _plain("Unlimited"), "value": _UNLIMITED}] + [
        {"text": _plain(str(n)), "value": str(n)} for n in range(1, MAX_OPTIONS)
    ]
    blocks.extend(
        [
            {"type": "divider"},
            {
                "type": "input",
                "block_id": SETTINGS_BLOCK,
                "optional": True,
                "label": _plain("Settings"),
                "element": {
                    "type": "checkboxes",
                    "action_id": _VALUE,
                    "options": [
                        {
                            "text": _plain("Anonymous votes"),
                            "description": _plain("Show counts, not who voted"),
                            "value": _ANONYMOUS,
                        }
                    ],
                },
            },
            {
                "type": "input",
                "block_id": LIMIT_BLOCK,
                "label": _plain("Votes per person"),
                "element": {
                    "type": "static_select",
                    "action_id": _VALUE,
                    "options": limit_choices,
                    "initial_option": limit_choices[0],
                },
            },
        ]
    )

    return {
        "type": "modal",
        "callback_id": POLL_MODAL_CALLBACK,
        "private_metadata": metadata.dumps(),
        "title": _plain("Create a poll"),
        "submit": _plain("Create poll"),
        "close": _plain("Cancel"),
        "blocks": blocks,
    }


def parse_poll_modal(view: dict) -> PollSpec:
    """Build a PollSpec from a submitted modal.

    Raises PollModalError keyed by block_id so errors render next to the input.
    """
    values = view["state"]["values"]

    def text_of(block_id: str) -> str:
        return (values.get(block_id, {}).get(_VALUE, {}).get("value") or "").strip()

    question = text_of(QUESTION_BLOCK)
    option_blocks = sorted(
        (b for b in values if b.startswith(OPTION_BLOCK_PREFIX)),
        key=lambda b: int(b.removeprefix(OPTION_BLOCK_PREFIX)),
    )
    options = [o for o in (text_of(b) for b in option_blocks) if o]

    selected = values.get(SETTINGS_BLOCK, {}).get(_VALUE, {}).get("selected_options")
    anonymous = any(o["value"] == _ANONYMOUS for o in selected or [])

    limit_option = values.get(LIMIT_BLOCK, {}).get(_VALUE, {}).get("selected_option")
    limit_value = limit_option["value"] if limit_option else _UNLIMITED
    vote_limit = None if limit_value == _UNLIMITED else int(limit_value)

    try:
        return build_poll_spec(question, options, anonymous, vote_limit)
    except PollParseError as e:
        block = QUESTION_BLOCK if e.field == "question" else f"{OPTION_BLOCK_PREFIX}0"
        raise PollModalError({block: str(e)}) from e


class PollModalError(ValueError):
    def __init__(self, errors: dict[str, str]):
        super().__init__(errors)
        # block_id -> message, the shape of a view_submission "errors" response
        self.errors = errors
