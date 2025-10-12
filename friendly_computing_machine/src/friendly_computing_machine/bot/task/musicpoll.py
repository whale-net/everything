import logging
import re
from datetime import UTC, datetime, timedelta
from typing import Optional

from friendly_computing_machine.bot.app import (
    get_bot_config,
    get_slack_web_client,
)
from friendly_computing_machine.bot.slack_client import SlackWebClientFCM
from friendly_computing_machine.bot.task.abstracttask import (
    AbstractTask,
    OneOffTask,
    ScheduledAbstractTask,
)
from friendly_computing_machine.bot.util import slack_send_message
from friendly_computing_machine.db.dal import (
    find_poll_instance_messages,
    get_unprocessed_music_poll_instances,
    insert_music_poll_instance,
    insert_music_poll_responses,
    upsert_message,
)
from friendly_computing_machine.db.jobsql import (
    backfill_init_music_poll_instances,
    backfill_init_music_polls,
    backfill_music_poll_instance_next_id,
    backfill_slack_messages_slack_channel_id,
    backfill_slack_messages_slack_team_id,
    backfill_slack_messages_slack_user_id,
    delete_slack_message_duplicates,
)
from friendly_computing_machine.models.music_poll import (
    MusicPollInstance,
    MusicPollResponseCreate,
)
from friendly_computing_machine.models.slack import SlackMessageCreate
from friendly_computing_machine.models.task import TaskInstanceStatus

logger = logging.getLogger(__name__)


class MusicPollPostPoll(ScheduledAbstractTask):
    OLD_TEMPLATE: str = """:catjam: `IT'S` :catjam: `CAT` :catjam: `JAM` :catjam: `TIME` :catjam:

:arrow_down_small: Post a song in the thread below :thread: :arrow_down_small: :whale:
"""

    def _run(self) -> TaskInstanceStatus:
        # TODO - templates from db

        # it is convenient to have same shared cache-type object as the event listener thread
        # ignore cache, since this job runs less frequently
        config = get_bot_config(should_ignore_cache=True)
        logger.info("music poll info %s", config.music_poll_infos)
        for poll_info in config.music_poll_infos:
            # all polls must have a message to start. For some reason.
            poll_message = slack_send_message(
                poll_info.slack_channel.slack_id,
                message=":catjam: any cat jammers? :catjam:",
            )
            logger.info("sent poll message id=%s", poll_message.id)
            insert_music_poll_instance(
                poll_info.music_poll.to_instance(poll_message.id)
            )

            backfill_music_poll_instance_next_id()

            # TODO - process the most recent poll here

            # No need to start thread anymore
            # slack_send_message(
            #     channel_slack_id, ":thread: starter", thread_ts=base_message.ts
            # )

        return TaskInstanceStatus.OK

    @property
    def period(self) -> timedelta:
        return timedelta(days=7)

    @property
    def start_date(self) -> datetime:
        return datetime(2024, 11, 30)


class MusicPollProcessPoll(AbstractTask):
    URL_PATTERN = re.compile(
        r"(?:http[s]?://|www\.)(?:[a-zA-Z]|[0-9]|[$-_@.&+]|[!*\\(\\),]|(?:%[0-9a-fA-F][0-9a-fA-F]))+"
    )

    @property
    def period(self) -> timedelta:
        # this should really be set to run once the post poll is complete
        # but doing this spinning style for now
        # TODO - dependent tasks
        return timedelta(hours=1)

    def _run(self) -> TaskInstanceStatus:
        # find unprocessed polls
        # TODO - live poll processing - maybe better suited for the event handler
        # TODO - this will pick up polls that had no responses
        #   need some other way to track this state such as a new field
        #   this could also help simplify the above todo and all sql
        instances_to_process = get_unprocessed_music_poll_instances()

        for poll_instance in instances_to_process:
            logger.info("processing poll instance %s", poll_instance.id)
            MusicPollProcessPoll._process_poll_instance(poll_instance)

        return TaskInstanceStatus.OK

    @staticmethod
    def _process_poll_instance(poll_instance: MusicPollInstance):
        messages = find_poll_instance_messages(poll_instance)
        for message in messages:
            # Extract URLs from message text
            urls = re.findall(MusicPollProcessPoll.URL_PATTERN, message.text)
            responses = [
                MusicPollResponseCreate(
                    music_poll_instance_id=poll_instance.id,
                    slack_message_id=message.id,
                    slack_user_id=message.slack_user_id,
                    url=url,
                    # TODO - this should be whatever timezone the db is in
                    # hopefully there is a conversion
                    # but for now this field at least doesn't matter
                    created_at=datetime.now(),
                )
                for url in urls
            ]
            insert_music_poll_responses(responses)


class MusicPollInit(OneOffTask):
    """
    The music poll response logging was added before any musicpoll database stuff was added.
    So this will initialize the database with the past polls
    """

    def _run(self) -> TaskInstanceStatus:
        backfill_init_music_polls()
        logger.info("music poll backfilled for init")
        backfill_init_music_poll_instances()
        logger.info("music poll instance backfilled for init")
        return TaskInstanceStatus.OK


# TODO - this should probably not be a job, and instead moved into the music poll processor
class MusicPollArchiveMessages(AbstractTask):
    """
    Archive the messages in the music poll channels
    """

    @property
    def period(self) -> timedelta:
        return timedelta(days=1)

    def _run(self) -> TaskInstanceStatus:
        delete_slack_message_duplicates()

        bot_config = get_bot_config(should_ignore_cache=True)
        # TODO - this is the same logic as the event handler for now
        archive_channel_slack_ids = {
            info.slack_channel.slack_id for info in bot_config.music_poll_infos
        }

        slack_client = get_slack_web_client()
        # free slack is 90 days, no need for more
        # one less to avoid any odd off by one errors
        search_start_unix_timestamp = (
            datetime.now(UTC) - timedelta(days=89)
        ).timestamp()
        for channel_slack_id in archive_channel_slack_ids:
            MusicPollArchiveMessages._archive_slack_channel(
                channel_slack_id,
                slack_client,
                max_ts_offset=search_start_unix_timestamp,
            )
        logger.info("primary message backfill completed")

        # run extra backfill tasks
        backfill_slack_messages_slack_channel_id()
        backfill_slack_messages_slack_user_id()
        backfill_slack_messages_slack_team_id()
        logger.info("extra backfill tasks completed")

        return TaskInstanceStatus.OK

    @staticmethod
    def _archive_slack_channel(
        slack_channel_slack_id: str,
        slack_client: SlackWebClientFCM,
        max_ts_offset: Optional[float] = None,
    ) -> None:
        if max_ts_offset is None:
            # 30 day lookback by default, should be enough
            # gives 30 days to catch this
            max_ts_offset = (datetime.now(UTC) - timedelta(days=30)).timestamp()

        def archive_message_batch(
            m_batch, t_batch
        ) -> tuple[list[dict], list[dict], list[int]]:
            """
            Archive a batch of messages
            :param m_batch: message batch incoming
            :param t_batch: thread batch incoming
            :return:
            """
            ids = []
            thread_batch = []
            msg_batch = m_batch + t_batch
            for msg in msg_batch:
                # upsert the message
                create = SlackMessageCreate.from_slack_message_json(
                    msg,
                    # specify because we pulled it down and I don't think it's included in the response
                    slack_channel_slack_id=slack_channel_slack_id,
                    team_slack_id=slack_client.team_id,
                )
                # this is too much info but was useful for debug, although not useful enough
                # logger.info('upserting message %s %s %s', create.slack_id, create.ts, create.text)
                message_instance = upsert_message(create)
                ids.append(message_instance.id)
                logger.debug("upserted message %s", message_instance.id)

                # if this is a thread message, pick up replies
                if msg.get("thread_ts") is not None and msg.get("ts") == msg.get(
                    "thread_ts"
                ):
                    message_replies = slack_client.conversations_replies(
                        channel=slack_channel_slack_id,
                        ts=msg.get("thread_ts"),
                    )
                    # remove the root thread message from this batch
                    reply_messages = [
                        message
                        for message in (message_replies["messages"] or [])
                        if message.get("ts") != msg.get("thread_ts")
                    ]
                    logger.info("found %s thread messages", len(reply_messages))
                    thread_batch += reply_messages

            # get the next batch
            # if we have no messages, we are done
            oldest_ts = (
                min(map(lambda msg: float(msg["ts"]), m_batch))
                if len(m_batch) > 0
                else max_ts_offset
            )
            next_fetch = MusicPollArchiveMessages._fetch_slack_messages(
                slack_channel_slack_id,
                slack_client,
                oldest_ts=max_ts_offset,
                latest_ts=oldest_ts,
            )
            logger.info("fetched %s messages", len(next_fetch))
            return next_fetch, thread_batch, ids

        message_batch = MusicPollArchiveMessages._fetch_slack_messages(
            slack_channel_slack_id,
            slack_client,
            oldest_ts=max_ts_offset,
        )
        thread_batch = []
        while len(message_batch) > 0 or len(thread_batch) > 0:
            message_batch, thread_batch, updated_ids = archive_message_batch(
                message_batch, thread_batch
            )
            logger.info("upserted %s messages", len(updated_ids))

    @staticmethod
    def _fetch_slack_messages(
        slack_channel_slack_id: str,
        slack_client: SlackWebClientFCM,
        oldest_ts: float,
        latest_ts: Optional[float | str] = None,
    ) -> list[dict]:
        if oldest_ts == latest_ts:
            return []
        response = slack_client.conversations_history(
            channel=slack_channel_slack_id,
            latest=str(latest_ts) if latest_ts is not None else None,
            oldest=str(oldest_ts),
        )
        return response["messages"] or []
