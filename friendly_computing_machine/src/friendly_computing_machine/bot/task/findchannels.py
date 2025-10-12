from datetime import timedelta

from friendly_computing_machine.src.friendly_computing_machine.bot.task.abstracttask import AbstractTask
from friendly_computing_machine.src.friendly_computing_machine.db.jobsql import (
    backfill_slack_messages_slack_channel_id,
)
from friendly_computing_machine.src.friendly_computing_machine.models.task import TaskInstanceStatus


class ChannelUpdateTask(AbstractTask):
    @property
    def period(self) -> timedelta:
        return timedelta(minutes=5)

    def _run(self) -> TaskInstanceStatus:
        backfill_slack_messages_slack_channel_id()
        return TaskInstanceStatus.OK
