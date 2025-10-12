import logging
from dataclasses import dataclass
from typing import Any, Dict, Optional

logger = logging.getLogger(__name__)


@dataclass
class ShortcutPayload:
    trigger_id: str
    user_id: str

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "ShortcutPayload":
        return cls(
            trigger_id=data["trigger_id"],
            user_id=data["user"]["id"]
            if "user" in data and "id" in data["user"]
            else "",
        )


@dataclass
class ViewSubmissionPayload:
    trigger_id: str
    selected_server: Optional[str] = None

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "ViewSubmissionPayload":
        try:
            selected_server = data["view"]["state"]["values"]["server_select_block"][
                "server_select_action"
            ]["selected_option"]["value"]
        except Exception:
            selected_server = None
        return cls(trigger_id=data["trigger_id"], selected_server=selected_server)


@dataclass
class ActionPayload:
    _ = """
    # Common fields in action payloads:
    {
        "type": "block_actions",  # or "view_submission", "view_closed"
        "user": {
            "id": "U123456",
            "name": "username",
            "team_id": "T123456"
        },
        "api_app_id": "A123456",
        "token": "verification_token",
        "container": {
            "type": "view",  # or "message"
            "view_id": "V123456"
        },
        "trigger_id": "123456.789.abcdef",  # For opening modals
        "team": {
            "id": "T123456",
            "domain": "workspace-name"
        },
        "enterprise": {...},  # If using Enterprise Grid
        "is_enterprise_install": false,
        "view": {
            "id": "V123456",
            "team_id": "T123456",
            "type": "modal",
            "title": {...},
            "close": {...},
            "submit": {...},
            "private_metadata": "your-data-here",
            "callback_id": "your_callback_id",
            "state": {
                "values": {
                    # Your input values here
                }
            },
            "hash": "123456.abcdef",
            "root_view_id": "V123456",
            "previous_view_id": null,
            "app_id": "A123456",
            "app_installed_team_id": "T123456",
            "bot_id": "B123456"
        },
        "actions": [  # For block_actions type
            {
                "type": "button",
                "action_id": "button_1",
                "block_id": "block_1",
                "text": {...},
                "value": "click_me_123",
                "action_ts": "1234567890.123456"
            }
        ],
        "response_urls": [  # For sending follow-up messages
            {
                "response_url": "https://hooks.slack.com/actions/...",
                "block_id": "block_1",
                "action_id": "button_1"
            }
        ]
    }
    """

    user_id: str

    # private metadata is used to store the action body for views
    private_metadata: str | None
    # otherwise it's the action body
    action_value: str | None

    stdin_command_input: Optional[str] = None
    view_id: Optional[str] = None

    @property
    def action_body(self) -> Any:
        if self.private_metadata and self.action_value:
            logger.warning(
                "Both private_metadata and action_body are set. "
                "Returning private_metadata."
            )
        elif self.private_metadata:
            return self.private_metadata
        elif self.action_value:
            return self.action_value
        return None

    @classmethod
    def from_dict(cls, data: Dict[str, Any]) -> "ActionPayload":
        try:
            view_id = data["view"]["id"]
        except Exception:
            view_id = None

        try:
            private_metadata = data["view"]["private_metadata"]
        except Exception:
            private_metadata = None

        try:
            action_value = data["actions"][0]["value"]
        except Exception:
            action_value = None

        try:
            stdin_command_input = data["view"]["state"]["values"][
                "stdin_custom_input_block"
            ]["stdin_custom_command_input"]["value"]
        except Exception:
            # fallback action_value because it likely means it's coming from action
            # this whole stdin_command_input = is a bit of hack, but it works for now
            stdin_command_input = action_value

        return cls(
            user_id=data["user"]["id"],
            stdin_command_input=stdin_command_input,
            private_metadata=private_metadata,
            action_value=action_value,
            view_id=view_id,
        )
