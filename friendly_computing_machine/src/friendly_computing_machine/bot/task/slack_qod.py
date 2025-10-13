from datetime import timedelta

from friendly_computing_machine.src.friendly_computing_machine.bot.task.abstracttask import AbstractTask
from friendly_computing_machine.src.friendly_computing_machine.db.jobsql import delete_slack_message_duplicates
from friendly_computing_machine.src.friendly_computing_machine.models.task import TaskInstanceStatus


class SlackMessageDuplicateCleanup(AbstractTask):
    @property
    def period(self) -> timedelta:
        return timedelta(hours=1)

    def _run(self) -> TaskInstanceStatus:
        delete_slack_message_duplicates()
        return TaskInstanceStatus.OK
