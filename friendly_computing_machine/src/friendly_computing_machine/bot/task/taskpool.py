import logging
import time
from datetime import timedelta

from friendly_computing_machine.bot.task.abstracttask import AbstractTask, OneOffTask
from friendly_computing_machine.bot.task.musicpoll import (
    MusicPollArchiveMessages,
    MusicPollInit,
    MusicPollPostPoll,
    MusicPollProcessPoll,
)
from friendly_computing_machine.db.dal import insert_task_instances
from friendly_computing_machine.models.task import TaskInstanceStatus

logger = logging.getLogger(__name__)


class TaskPool:
    def __init__(
        self, sleep_period=timedelta(seconds=5), log_skipped_tasks: bool = False
    ):
        # using list, manual dupe check
        # self._tasks: set[AbstractTask] = set()
        self._tasks: list[AbstractTask] = []
        self._sleep_period_seconds = sleep_period.total_seconds()
        self.__should_run = True
        self._is_finalized: bool = False
        self._log_skipped_tasks = log_skipped_tasks
        logger.info("task pool init")

    def add_task(self, task: AbstractTask):
        if self._is_finalized:
            raise RuntimeError("task pool already finalized")
        # self._tasks.add(task)
        if task in self._tasks:
            logger.warning("task already in pool, skipping add")
            return
        self._tasks.append(task)

    def finalize(self):
        # get task config for the pool
        # for now, just everything

        # sort tasks by oneoff. OneOff should run before anything scheduled
        one_off_tasks = [task for task in self._tasks if isinstance(task, OneOffTask)]
        regular_tasks = [
            task for task in self._tasks if not isinstance(task, OneOffTask)
        ]
        self._tasks = one_off_tasks + regular_tasks
        self._is_finalized = True
        logger.info("task pool is finalized, no more tasks can be created")

    def start(self):
        if not self._is_finalized:
            self.finalize()
        while self.__should_run:
            self._process_tasks()
            time.sleep(self._sleep_period_seconds)

    def _process_tasks(self):
        # TODO - thread pool this
        # most tasks will be API calls
        instances = []
        logger.debug("task pool will attempt to process %s tasks", len(self._tasks))
        for task in self._tasks:
            instances.append(task.run())

        if not self._log_skipped_tasks:
            instances = [
                instance
                for instance in instances
                if instance.status != TaskInstanceStatus.SKIPPED
            ]
        if len(instances) > 0:
            logger.info(f"processed {len(instances)} tasks")
            insert_task_instances(instances)

    def stop(self):
        """
        stop taskpool, intended to be externally triggered

        :return:
        """
        logger.info("taskpool stopping")
        self.__should_run = False


def create_default_taskpool() -> TaskPool:
    # unsure how to specify which tasks I actually want, so just going to make this register everything for now
    # with the option to comment out individual ones while I work on this

    # TODO - dependency structure of some sort
    # for now, just ordering corectly, and giving one off tasks priority
    tp = TaskPool()

    # These are the remaining tasks that run in the task pool
    # everything else is temporal
    tp.add_task(MusicPollPostPoll())
    tp.add_task(MusicPollInit())
    tp.add_task(MusicPollArchiveMessages())
    tp.add_task(MusicPollProcessPoll())

    # migrated to temporal
    # tp.add_task(SlackMessageDuplicateCleanup())
    # tp.add_task(FindTeams())
    # tp.add_task(FindUsers())
    # tp.add_task(ChannelUpdateTask())
    # tp.add_task(GenAISlackIDUpdateTask())

    logger.info("default task pool created")
    return tp
