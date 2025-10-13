from datetime import timedelta

from friendly_computing_machine.src.friendly_computing_machine.bot.task.abstracttask import AbstractTask
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    select_distinct_slack_team_slack_id_from_slack_message,
    upsert_slack_teams,
)
from friendly_computing_machine.src.friendly_computing_machine.db.jobsql import backfill_slack_messages_slack_team_id
from friendly_computing_machine.src.friendly_computing_machine.models.slack import SlackTeamCreate
from friendly_computing_machine.src.friendly_computing_machine.models.task import TaskInstanceStatus


class FindTeams(AbstractTask):
    @property
    def period(self) -> timedelta:
        return timedelta(hours=12)

    def _run(self) -> TaskInstanceStatus:
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
        backfill_slack_messages_slack_team_id()
        return TaskInstanceStatus.OK
