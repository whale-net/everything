"""The weekly music-poll activities (krill M5).

The selection activity picks a channel's options; the
activities here read the channel list, read and write a
run's scheduledpollrun history, and post a recorded run's
poll -- closing the channel's previous scheduled poll
first -- so the workflow that calls them only orchestrates.
"""

import datetime
import logging
import random
from dataclasses import dataclass

from sqlalchemy.exc import IntegrityError
from sqlmodel import Session, select
from temporalio import activity

from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    get_slack_web_client,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    PollSpec,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.poll.render import (
    render_poll_blocks,
    render_poll_text,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_dal import (
    get_music_poll_channels,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.music_poll_selection import (
    SelectedOption,
    select_scheduled_poll_options,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    PollSnapshot,
    close_poll,
)
from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
)
from friendly_computing_machine.src.friendly_computing_machine.models.scheduled_poll import (
    ScheduledPollRun,
    ScheduledPollRunOption,
)
from friendly_computing_machine.src.friendly_computing_machine.poll.publish import (
    publish_scheduled_poll,
)

logger = logging.getLogger(__name__)

# the question of the poll a weekly run posts
WEEKLY_POLL_QUESTION = "Weekly music throwback"


@activity.defn
async def select_music_poll_options_activity(
    slack_channel_slack_id: str,
) -> list[SelectedOption] | None:
    """Pick this week's poll options for one music-poll channel.

    The selection, the clock and the tie-break randomness all
    live inside the activity, so the workflow that calls it only
    orchestrates: it posts the options this returns, or -- when
    the activity returns None because fewer than three songs
    were pickable -- leaves the previous poll open and skips
    the week.
    """
    options = select_scheduled_poll_options(
        slack_channel_slack_id,
        datetime.datetime.now(datetime.UTC),
        rng=random.Random(),
    )
    if options is None:
        logger.info(
            "weekly music poll skipped for %s: fewer than 3 pickable songs",
            slack_channel_slack_id,
        )
    else:
        logger.info(
            "weekly music poll picked %s options for %s",
            len(options),
            slack_channel_slack_id,
        )
    return options


@dataclass
class ScheduledPollRunRow:
    """A run's scheduledpollrun row, with the options it recorded."""

    id: int
    run_identity: str
    slack_channel_slack_id: str
    run_at: datetime.datetime
    scheduled_fire_time: datetime.datetime | None
    workflow_run_id: str | None
    poll_id: int | None
    slack_message_ts: str | None
    options: list[SelectedOption]


@dataclass
class PostScheduledPollOutcome:
    """What posting one channel's scheduled poll did."""

    poll_id: int | None
    slack_message_ts: str | None
    # the previous scheduled poll this run closed, if any
    closed_previous_poll_id: int | None
    error: str | None


@activity.defn
async def get_music_poll_channels_activity() -> list[str]:
    """Slack ids of every channel configured for weekly music polls."""
    return [
        music_poll_channel.slack_channel.slack_id
        for music_poll_channel in get_music_poll_channels()
    ]


@activity.defn
async def get_scheduled_poll_run_activity(
    run_identity: str, slack_channel_slack_id: str
) -> ScheduledPollRunRow | None:
    """The row a run recorded in a channel, with its options."""
    with SessionManager() as session:
        run = session.exec(
            select(ScheduledPollRun).where(
                ScheduledPollRun.run_identity == run_identity,
                ScheduledPollRun.slack_channel_slack_id
                == slack_channel_slack_id,
            )
        ).first()
        if run is None:
            return None
        option_rows = session.exec(
            select(ScheduledPollRunOption)
            .where(
                ScheduledPollRunOption.scheduled_poll_run_id == run.id
            )
            .order_by(ScheduledPollRunOption.position)
        ).all()
        return ScheduledPollRunRow(
            id=run.id,
            run_identity=run.run_identity,
            slack_channel_slack_id=run.slack_channel_slack_id,
            run_at=run.run_at,
            scheduled_fire_time=run.scheduled_fire_time,
            workflow_run_id=run.workflow_run_id,
            poll_id=run.poll_id,
            slack_message_ts=run.slack_message_ts,
            options=[
                SelectedOption(
                    song_identity=option_row.song_identity,
                    song_link=option_row.song_link,
                    submitter_slack_user_slack_id=(
                        option_row.submitter_slack_user_slack_id
                    ),
                    submission_date=option_row.submission_date,
                )
                for option_row in option_rows
            ],
        )


@activity.defn
async def record_scheduled_poll_run_activity(
    run_identity: str,
    slack_channel_slack_id: str,
    run_at: datetime.datetime,
    scheduled_fire_time: datetime.datetime | None,
    workflow_run_id: str | None,
    options: list[SelectedOption],
) -> int:
    """Write a run's history row and its option rows.

    Written before the Slack post, keyed by (run identity,
    channel), so a retried or replayed run can tell its
    poll was already posted. A second writer that loses
    the unique-key race reads back the row the first
    writer committed.
    """
    with SessionManager() as session:
        try:
            run = ScheduledPollRun(
                run_identity=run_identity,
                slack_channel_slack_id=slack_channel_slack_id,
                run_at=run_at,
                scheduled_fire_time=scheduled_fire_time,
                workflow_run_id=workflow_run_id,
            )
            session.add(run)
            session.flush()
            for position, option in enumerate(options):
                session.add(
                    ScheduledPollRunOption(
                        scheduled_poll_run_id=run.id,
                        position=position,
                        song_identity=option.song_identity,
                        song_link=option.song_link,
                        submitter_slack_user_slack_id=(
                            option.submitter_slack_user_slack_id
                        ),
                        submission_date=option.submission_date,
                    )
                )
            session.commit()
            return run.id
        except IntegrityError:
            session.rollback()
            existing = session.exec(
                select(ScheduledPollRun).where(
                    ScheduledPollRun.run_identity == run_identity,
                    ScheduledPollRun.slack_channel_slack_id
                    == slack_channel_slack_id,
                )
            ).first()
            if existing is None:
                raise
            return existing.id


@activity.defn
async def post_scheduled_poll_activity(
    slack_channel_slack_id: str,
    scheduled_poll_run_id: int,
    options: list[SelectedOption],
) -> PostScheduledPollOutcome:
    """Post one channel's poll for a recorded run.

    Closes the channel's previous scheduled poll if it is
    still open, publishes this run's poll through the
    bolt-free publish module, and stamps the run's history
    rows with the created poll's ids. The posted marker is
    re-checked here too, so a retry that arrives past a
    crash between recording the run and posting neither
    posts nor closes again.
    """
    client = get_slack_web_client()

    with SessionManager() as session:
        run = session.get(ScheduledPollRun, scheduled_poll_run_id)
        if run is None:
            raise RuntimeError(
                f"no scheduled poll run {scheduled_poll_run_id} to post"
            )
        if run.slack_message_ts is not None:
            # an earlier attempt of this run already posted
            return PostScheduledPollOutcome(
                poll_id=run.poll_id,
                slack_message_ts=run.slack_message_ts,
                closed_previous_poll_id=None,
                error=None,
            )
        closed_previous_poll_id = _close_previous_scheduled_poll(
            session, client, slack_channel_slack_id
        )

    spec = PollSpec(
        question=WEEKLY_POLL_QUESTION,
        options=[option.song_link for option in options],
        anonymous=False,
        # the weekly poll is single-vote and not anonymous
        vote_limit=1,
    )
    post = publish_scheduled_poll(
        client, spec, slack_channel_slack_id, _bot_slack_user_id(client)
    )
    if isinstance(post, str):
        logger.error(
            "weekly music poll post failed in %s: %s",
            slack_channel_slack_id,
            post,
        )
        return PostScheduledPollOutcome(
            poll_id=None,
            slack_message_ts=None,
            closed_previous_poll_id=closed_previous_poll_id,
            error=post,
        )

    with SessionManager() as session:
        # stamp the run row and its option rows with the
        # created poll's ids, so the run's picks stay
        # queryable after the poll closes
        run = session.get(ScheduledPollRun, scheduled_poll_run_id)
        run.poll_id = post.poll_id
        run.slack_message_ts = post.slack_message_ts
        poll_option_ids = {
            option.position: option.id
            for option in session.exec(
                select(PollOption)
                .where(PollOption.poll_id == post.poll_id)
                .order_by(PollOption.position)
            )
        }
        for option_row in session.exec(
            select(ScheduledPollRunOption).where(
                ScheduledPollRunOption.scheduled_poll_run_id
                == scheduled_poll_run_id
            )
        ):
            option_row.poll_option_id = poll_option_ids.get(
                option_row.position
            )
        session.commit()

    return PostScheduledPollOutcome(
        poll_id=post.poll_id,
        slack_message_ts=post.slack_message_ts,
        closed_previous_poll_id=closed_previous_poll_id,
        error=None,
    )


def _close_previous_scheduled_poll(
    session: Session,
    client: SlackWebClientFCM,
    slack_channel_slack_id: str,
) -> int | None:
    """Close the channel's previous scheduled poll if still open.

    Only a poll a run posted is closed -- a poll a person
    created with /wpoll is never touched -- and a poll that
    is already closed stays as it is.
    """
    run = session.exec(
        select(ScheduledPollRun)
        .where(
            ScheduledPollRun.slack_channel_slack_id
            == slack_channel_slack_id,
            ScheduledPollRun.poll_id.is_not(None),
        )
        .order_by(
            ScheduledPollRun.run_at.desc(), ScheduledPollRun.id.desc()
        )
        .limit(1)
    ).first()
    if run is None or run.poll_id is None:
        return None
    poll = session.get(Poll, run.poll_id)
    if poll is None or not poll.automated or poll.closed_at is not None:
        return None
    snapshot = close_poll(poll.id, session=session)
    _refresh_message(client, snapshot)
    logger.info(
        "closed previous scheduled poll %s in %s",
        poll.id,
        slack_channel_slack_id,
    )
    return poll.id


def _refresh_message(client: SlackWebClientFCM, snapshot: PollSnapshot) -> None:
    poll = snapshot.poll
    if poll.slack_message_ts is None:
        logger.warning("poll %s has no message to update", poll.id)
        return
    client.chat_update(
        channel=poll.slack_channel_slack_id,
        ts=poll.slack_message_ts,
        text=render_poll_text(snapshot),
        blocks=render_poll_blocks(snapshot),
    )


def _bot_slack_user_id(client: SlackWebClientFCM) -> str:
    """The bot's own Slack user id, the creator of a scheduled poll."""
    return client.auth_test()["user_id"]
