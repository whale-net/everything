import logging
from datetime import timedelta

from friendly_computing_machine.bot.task.abstracttask import AbstractTask
from friendly_computing_machine.models.task import TaskInstanceStatus

logger = logging.getLogger(__name__)


class FindUsers(AbstractTask):
    @property
    def period(self) -> timedelta:
        return timedelta(minutes=5)

    def _run(self) -> TaskInstanceStatus:
        # temp remove while deprecation
        # slack_client = get_slack_web_client()
        # slack_client.team_info()
        # slack_user_team_pairs = get_user_teams_from_messages(
        #     slack_team_slack_id=slack_client.team_id
        # )
        # slack_user_creates = []
        # for slack_user_slack_id, slack_team_slack_id in slack_user_team_pairs:
        #     try:
        #         slack_user_profile_response = slack_client.users_profile_get(
        #             user=slack_user_slack_id
        #         )
        #         if slack_user_profile_response.status_code != 200:
        #             raise RuntimeError(f"status not 200 {slack_user_profile_response}")
        #         slack_user_profile = slack_user_profile_response.get("profile")
        #         slack_user_creates.append(
        #             SlackUserCreate(
        #                 slack_id=slack_user_slack_id,
        #                 name=slack_user_profile.get("display_name")
        #                 or slack_user_profile.get("real_name"),
        #                 slack_team_slack_id=slack_team_slack_id,
        #             )
        #         )
        #     except Exception as e:
        #         logger.exception(e)

        # upsert_slack_users(slack_user_creates)
        # backfill_slack_messages_slack_user_id()
        return TaskInstanceStatus.OK
