import logging
import re
from dataclasses import dataclass

from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_slack_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    get_genai_texts_by_slack_channel,
    get_user_teams_from_messages,
)
from friendly_computing_machine.src.friendly_computing_machine.models.genai import (
    GenAIText,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackUserCreate,
)

logger = logging.getLogger(__name__)


# TODO - put this in db.dal_activity (or just activity? or activity.dal and)
@activity.defn
async def get_slack_channel_context(slack_channel_slack_id: str) -> list[GenAIText]:
    texts = get_genai_texts_by_slack_channel(slack_channel_slack_id)
    return texts


@dataclass
class GenerateContextPromptParams:
    """
    Parameters for the generate_context_prompt activity.
    """

    prompt_text: str
    previous_context: str
    vibe: str


@activity.defn
async def generate_context_prompt(params: GenerateContextPromptParams) -> str:
    """
    Generate a context prompt based on previous messages and the current prompt.
    """
    tone_text = f"Detected tone of incoming prompt: {params.vibe}\n"

    context_prompt = (
        "# Response Guidelines\n\n"
        "## Context Information\n"
        f"Previous conversation summary:\n{params.previous_context}\n\n"
        "## The Vibe\n"
        f"{tone_text}\n"
        "## User Prompt\n"
        f"{params.prompt_text}\n\n"
        "## Additional Instructions\n"
        "- Consider the previous conversation context when relevant, but don't explicitly reference it\n"
        "- Keep responses concise (100-150 words) unless the user specifically requests a longer answer\n"
        "- Be helpful, accurate, and engaging in your response\n"
        "- Format your response appropriately for the question type\n"
        "- Please consider The Vibe when crafting your response\n"
    )
    return context_prompt


@activity.defn
async def backfill_slack_user_info_activity() -> list[SlackUserCreate]:
    """
    Backfill slack user info.
    """

    slack_client = get_slack_web_client()
    slack_client.team_info()
    slack_user_team_pairs = get_user_teams_from_messages(
        slack_team_slack_id=slack_client.team_id
    )
    slack_user_creates = []
    for slack_user_slack_id, slack_team_slack_id in slack_user_team_pairs:
        try:
            slack_user_profile_response = slack_client.users_profile_get(
                user=slack_user_slack_id
            )
            if slack_user_profile_response.status_code != 200:
                raise RuntimeError(f"status not 200 {slack_user_profile_response}")
            slack_user_profile = slack_user_profile_response.get("profile")
            slack_user_creates.append(
                SlackUserCreate(
                    slack_id=slack_user_slack_id,
                    name=slack_user_profile.get("display_name")
                    or slack_user_profile.get("real_name"),
                    slack_team_slack_id=slack_team_slack_id,
                )
            )
        except Exception as e:
            logger.exception(e)

    # TODO - persist these some other way to avoid passing back via temporal?
    # maybe not so bad for my purposes, but definitely not scalable
    return slack_user_creates


@dataclass
class FixSlackTaggingParams:
    """
    Parameters for the fix_slack_tagging_activity.
    """

    text: str
    add_here_tag: bool = False


@activity.defn
async def fix_slack_tagging_activity(params: FixSlackTaggingParams) -> str:
    """
    Fix slack tagging in the text.
    This function replaces @here and @channel with their escaped versions.
    If add_here_tag is True, adds @here at the beginning of the message.
    TODO: It also replaces <@U12345> with @U12345.
    """

    # TODO - person name replacement

    # Add @here tag if this is a call to action
    if params.add_here_tag and not params.text.strip().startswith("<!here>"):
        params.text = f"<!here> {params.text}"

    # Replace @here and @channel with their escaped versions only if not already escaped
    params.text = re.sub(r"(?<!<)@here", "<!here>", params.text)
    params.text = re.sub(r"(?<!<)@channel", "<!channel>", params.text)

    return params.text
