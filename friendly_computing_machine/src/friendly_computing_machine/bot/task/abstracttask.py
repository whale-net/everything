import logging
from abc import ABC, abstractmethod
from datetime import datetime, timedelta
from typing import Optional

from friendly_computing_machine.db.dal import (
    get_last_successful_task_instance,
    upsert_task,
)
from friendly_computing_machine.models.task import (
    Task,
    TaskCreate,
    TaskInstanceCreate,
    TaskInstanceStatus,
)

logger = logging.getLogger(__name__)


class AbstractTask(ABC):
    def __init__(self):
        """
        self.
        :param period:
        """

        # RBAR for each task, but that's fine for the volume
        self._task: Task = upsert_task(self.to_task_create())

        last_task_instance = get_last_successful_task_instance(self._task)
        logger.info("task %s last instance %s", self.task_name, last_task_instance)
        if last_task_instance is not None:
            self._last_success = last_task_instance.as_of
        else:
            logger.info(
                "task %s has no previous instance, defaulting to min date",
                self.task_name,
            )
            self._last_success = datetime.min
        # default to last success for now
        self._last_attempt = self._last_success

        # internal bool for basic synchronization
        # i think bool updates are atomic in python. at least I hope they are
        # not that i anticipate on running into this situation often; hopefully just in debug
        self._is_running = False

    def should_run(self) -> bool:
        if self._is_running:
            return False
        return self._last_attempt + self.period < datetime.now()

    def run(self, force_run: bool = False, *args, **kwargs) -> TaskInstanceCreate:
        """
        run the task if it should run

        :param force_run: bypass the should_run check
        :param args:
        :param kwargs:
        :return:
        """
        if force_run or self.should_run():
            try:
                logger.info("task %s is starting", self.task_name)
                self._is_running = True
                self._last_attempt = datetime.now()
                try:
                    status = self._run(*args, **kwargs)
                    logger.info("task %s has completed", self.task_name)
                except Exception as e:
                    logger.warning("task %s failed due to exception", self.task_name)
                    logger.exception(e)
                    status = TaskInstanceStatus.EXCEPTION
                # NOTE: this sets the last success, even if we had an exception
                # TODO last_success vs last_attempt
                #   this is kind of beneficial for debugging jobs locally
                self._last_success = datetime.now()
            finally:
                self._is_running = False
        else:
            logger.debug("task %s does not need to run", self.task_name)
            status = TaskInstanceStatus.SKIPPED

        return self.to_task_instance_create(status=status)

    @abstractmethod
    def _run(self, *args, **kwargs) -> TaskInstanceStatus:
        """
        implement this one, actually does the work
        :return:
        """
        pass

    @property
    @abstractmethod
    def period(self) -> timedelta:
        """
        the period you hope this task will run
        :return:
        """
        pass

    @property
    def task_name(self) -> str:
        return type(self).__name__

    def __hash__(self):
        return self.task_name.__hash__()

    def to_task_create(self) -> TaskCreate:
        # should probably be a class method, but whatever
        return TaskCreate(name=self.task_name)

    def to_task_instance_create(self, status: TaskInstanceStatus) -> TaskInstanceCreate:
        return TaskInstanceCreate(
            task_id=self._task.id,
            # for now, this datetime is set here
            as_of=datetime.now(),
            status=status,
        )


class ScheduledAbstractTask(AbstractTask):  # , ABC):
    def __init__(self):
        super().__init__()

    @property
    @abstractmethod
    def start_date(self) -> datetime:
        """
        When this should run. This date + period will be repeated indefinitely

        Do not use datetime.min, there is no purpose for that. Just use non-scheduled tasks

        """
        pass

    def should_run(self) -> bool:
        if self._is_running:
            return False

        last_expected_run = self.get_last_expected_run_datetime()
        # min signals no run expected
        # this is implicitly checked in the below condition, but better to be explicit
        if last_expected_run == datetime.min:
            return False

        # schedule a run if the last expected run is after our last success
        # this means a run is considered complete if there is any success after an expected run
        # this will allow us to catch up with at most 1 run and not allow any current delay to affect future runs
        return self._last_success < self.get_last_expected_run_datetime()

    def get_last_expected_run_datetime(self) -> datetime:
        """
        when did we expect last expect this function to run

        :return: returns min date if not expected to have ran yet
        """
        return ScheduledAbstractTask._get_last_expected_run_datetime(
            self.start_date, self.period
        )

    @staticmethod
    def _get_last_expected_run_datetime(
        start_date: datetime, period: timedelta, now: Optional[datetime] = None
    ) -> datetime:
        if now is None:
            now = datetime.now()
        # expose this as static for easier testing
        # if we are not past the start date, return min date
        # this signals "not expected to have run yet"
        if now < start_date:
            return datetime.min

        seconds_since_start = (now - start_date).total_seconds()
        expected_runs_since_start = seconds_since_start // period.total_seconds()
        return start_date + (period * expected_runs_since_start)


class OneOffTask(AbstractTask):
    """
    Represents a task that should run only once.
    Once it has been successfully executed, it will not run again.
    """

    def __init__(self):
        super().__init__()

    @property
    def period(self) -> timedelta:
        """
        One-off tasks don't have a real period, but we need to provide one
        for the AbstractTask interface. Using a large value effectively
        prevents re-runs based on time period.
        """
        return timedelta(days=36500)  # ~100 years

    def should_run(self) -> bool:
        """
        One-off task should run only if it has never succeeded before.
        """
        if self._is_running:
            return False

        # If _last_success is datetime.min, it means the task has never run successfully
        return self._last_success == datetime.min
