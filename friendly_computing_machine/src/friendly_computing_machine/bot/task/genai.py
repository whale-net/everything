from datetime import timedelta

from friendly_computing_machine.bot.task.abstracttask import AbstractTask
from friendly_computing_machine.db.jobsql import (
    backfill_genai_text_slack_channel_id,
    backfill_genai_text_slack_user_id,
)
from friendly_computing_machine.models.task import TaskInstanceStatus


class GenAISlackIDUpdateTask(AbstractTask):
    @property
    def period(self) -> timedelta:
        return timedelta(minutes=2)

    def _run(self) -> TaskInstanceStatus:
        backfill_genai_text_slack_user_id()
        backfill_genai_text_slack_channel_id()
        return TaskInstanceStatus.OK
