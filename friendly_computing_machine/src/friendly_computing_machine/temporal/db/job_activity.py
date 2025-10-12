import logging

from temporalio import activity

from friendly_computing_machine.db.dal import (
    select_distinct_slack_team_slack_id_from_slack_message,
    upsert_slack_teams,
    upsert_slack_users,
)
from friendly_computing_machine.db.jobsql import (
    backfill_genai_text_slack_channel_id,
    backfill_genai_text_slack_user_id,
    backfill_slack_messages_slack_channel_id,
    backfill_slack_messages_slack_team_id,
    backfill_slack_messages_slack_user_id,
    delete_slack_message_duplicates,
)
from friendly_computing_machine.models.slack import SlackTeamCreate, SlackUserCreate

logger = logging.getLogger(__name__)


@activity.defn
def backfill_slack_messages_slack_user_id_activity():
    """Backfills slack messages with slack user IDs."""
    logger.info("starting slack user id backfill")
    backfill_slack_messages_slack_user_id()
    return "OK"


@activity.defn
def backfill_slack_messages_slack_channel_id_activity():
    """Backfills slack messages with slack channel IDs."""
    logger.info("starting slack channel id backfill")
    backfill_slack_messages_slack_channel_id()
    return "OK"


@activity.defn
def backfill_slack_messages_slack_team_id_activity():
    """Backfills slack messages with slack team IDs."""
    logger.info("starting slack team id backfill")
    backfill_slack_messages_slack_team_id()
    return "OK"


@activity.defn
def delete_slack_message_duplicates_activity():
    """Delete duplicate slack messages."""
    logger.info("starting slack message duplicate deletion")
    delete_slack_message_duplicates()
    return "OK"


@activity.defn
async def backfill_teams_from_messages_activity():
    slack_team_slack_ids = select_distinct_slack_team_slack_id_from_slack_message()
    slack_team_creates = [
        SlackTeamCreate(
            slack_id=slack_team_id,
            # app.client.team_info
            name="i don't care enough to get the permissions to get the name",
        )
        for slack_team_id in slack_team_slack_ids
    ]

    upsert_slack_teams(slack_team_creates)


@activity.defn
async def upsert_slack_user_creates_activity(
    user_creates: list[SlackUserCreate],
) -> str:
    """
    Upsert slack users.
    """
    upsert_slack_users(user_creates)
    return "OK"


@activity.defn
def backfill_genai_text_slack_user_id_activity() -> str:
    """Backfill GenAI text records with missing Slack user IDs."""
    logger.info("starting genai text slack user id backfill")
    backfill_genai_text_slack_user_id()
    return "OK"


@activity.defn
def backfill_genai_text_slack_channel_id_activity() -> str:
    """Backfill GenAI text records with missing Slack channel IDs."""
    logger.info("starting genai text slack channel id backfill")
    backfill_genai_text_slack_channel_id()
    return "OK"
