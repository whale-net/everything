"""Weekly music-poll song selection (krill M5).

One run's whole pick runs through this module: the eligible pool
built from the channel's closed windows, the two exclusion rules
(the two most recent closed windows in chain order, and the last
eight run-posted polls), the submitter cap, and the recent/goldie
slot split. The Temporal activity wraps it so that all selection,
randomness and clock reads happen inside the activity while a
workflow only orchestrates.
"""

import datetime
import logging
import random
from collections import Counter
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from sqlmodel import Session, select

from friendly_computing_machine.src.friendly_computing_machine.db.util import (
    SessionManager,
)
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
    MusicPollInstance,
    MusicPollResponse,
)
from friendly_computing_machine.src.friendly_computing_machine.models.scheduled_poll import (
    ScheduledPollRun,
    ScheduledPollRunOption,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackMessage,
    SlackUser,
)

logger = logging.getLogger(__name__)

# a poll holds 3-4 options; fewer pickable songs skips the week
MIN_OPTION_COUNT = 3
MAX_OPTION_COUNT = 4
# the recent/goldie split the slots aim for
RECENT_SLOT_COUNT = 2
# a song is recent from 2 to 10 weeks old, a goldie older than that
RECENT_WEEKS_MIN = 2
RECENT_WEEKS_MAX = 10
# at most this many options from one submitter, and a second option
# from one submitter only once distinct submitters run out
MAX_OPTIONS_PER_SUBMITTER = 2
# how many of the most recent closed windows the grace period covers
GRACE_WINDOW_COUNT = 2
# how many of the most recent run-posted polls a song may not repeat in
NO_REPEAT_POLL_COUNT = 8

_SPOTIFY_TRACK_HOSTS = {"open.spotify.com"}
_YOUTUBE_VIDEO_HOSTS = {
    "youtube.com",
    "www.youtube.com",
    "m.youtube.com",
    "music.youtube.com",
}
_YOUTUBE_SHORT_PATHS = {"shorts", "embed", "live"}
# query parameters that carry nothing but the share's provenance
_TRACKING_QUERY_PARAMS = frozenset(
    {
        "utm_source",
        "utm_medium",
        "utm_campaign",
        "utm_term",
        "utm_content",
        "utm_id",
        "utm_source_platform",
        "utm_medium_platform",
        "fbclid",
        "gclid",
        "gclsrc",
        "msclkid",
        "mc_eid",
        "mc_cid",
        "_openstat",
        "ref",
        "source",
        "sk",
    }
)


def _spotify_track_id(url: str) -> str | None:
    parts = urlsplit(url)
    if (parts.hostname or "").lower() not in _SPOTIFY_TRACK_HOSTS:
        return None
    segments = parts.path.split("/")
    if len(segments) > 2 and segments[1] == "track":
        return segments[2] or None
    return None


def _youtube_video_id(url: str) -> str | None:
    parts = urlsplit(url)
    host = (parts.hostname or "").lower()
    if host == "youtu.be":
        return parts.path.strip("/").split("/")[0] or None
    if host not in _YOUTUBE_VIDEO_HOSTS:
        return None
    segments = parts.path.split("/")
    if len(segments) > 2 and segments[1] in _YOUTUBE_SHORT_PATHS:
        return segments[2] or None
    if parts.path == "/watch":
        return dict(parse_qsl(parts.query)).get("v")
    return None


def _stripped_url(url: str) -> str:
    parts = urlsplit(url)
    query = [
        (key, value)
        for key, value in parse_qsl(parts.query)
        if key.lower() not in _TRACKING_QUERY_PARAMS
    ]
    return urlunsplit(
        (parts.scheme, parts.netloc, parts.path, urlencode(query), "")
    )


def song_identity(url: str) -> str:
    """The normalized identity of a shared song.

    The Spotify track ID or YouTube video ID when the URL carries
    one, otherwise the URL with its tracking parameters stripped.
    """
    url = url.strip()
    if url.startswith("spotify:track:"):
        return f"spotify:{url.split(':', 2)[2]}"
    track_id = _spotify_track_id(url)
    if track_id:
        return f"spotify:{track_id}"
    video_id = _youtube_video_id(url)
    if video_id:
        return f"youtube:{video_id}"
    return _stripped_url(url)


@dataclass(frozen=True)
class SongSubmission:
    """One recorded musicpollresponse, joined to its Slack roots."""

    song_identity: str
    song_link: str
    submitter_slack_user_slack_id: str
    # when the submitting Slack message was originally posted, as UTC
    submission_date: datetime.datetime
    # the closed window the submission was recorded in
    music_poll_instance_id: int


@dataclass(frozen=True)
class SongCandidate:
    """An eligible song and how often past polls have featured it."""

    song_identity: str
    song_link: str
    submitter_slack_user_slack_id: str
    submission_date: datetime.datetime
    appearances: int


@dataclass(frozen=True)
class SelectedOption:
    """One option of the poll a run posts."""

    song_identity: str
    song_link: str
    submitter_slack_user_slack_id: str
    submission_date: datetime.datetime


def select_scheduled_poll_options(
    slack_channel_slack_id: str,
    now: datetime.datetime,
    session: Session | None = None,
    rng: random.Random | None = None,
) -> list[SelectedOption] | None:
    """Pick this week's poll options for one music-poll channel.

    Returns None when fewer than three songs are pickable, in which
    case no poll is posted for the channel and the previous poll
    stays open.
    """
    with SessionManager(session) as session:
        return _select_for_channel(
            session,
            slack_channel_slack_id,
            _as_utc(now),
            rng or random.Random(),
        )


def _select_for_channel(
    session: Session,
    slack_channel_slack_id: str,
    now: datetime.datetime,
    rng: random.Random,
) -> list[SelectedOption] | None:
    polls = session.exec(
        select(MusicPoll).join(
            SlackChannel, MusicPoll.slack_channel_id == SlackChannel.id
        ).where(SlackChannel.slack_id == slack_channel_slack_id)
    ).all()

    closed_instance_ids: set[int] = set()
    grace_instance_ids: set[int] = set()
    for poll in polls:
        instances = session.exec(
            select(MusicPollInstance).where(
                MusicPollInstance.music_poll_id == poll.id
            )
        ).all()
        closed, grace = _closed_and_grace_instance_ids(instances)
        closed_instance_ids |= closed
        grace_instance_ids |= grace

    submissions = _load_submissions(session, closed_instance_ids)
    if not submissions:
        return None

    appearances, no_repeat = _load_run_history(
        session, slack_channel_slack_id
    )

    # one song per identity, attributed to its earliest submission
    by_identity: dict[str, list[SongSubmission]] = {}
    for submission in submissions:
        by_identity.setdefault(submission.song_identity, []).append(
            submission
        )
    songs = [
        min(group, key=lambda s: s.submission_date)
        for group in by_identity.values()
    ]

    candidates = [
        SongCandidate(
            song_identity=song.song_identity,
            song_link=song.song_link,
            submitter_slack_user_slack_id=song.submitter_slack_user_slack_id,
            submission_date=song.submission_date,
            appearances=appearances[song.song_identity],
        )
        for song in songs
        # a song is ineligible when its earliest submission sits in
        # either of the two most recent closed windows, or when it
        # appeared as an option in one of the last eight run-posted
        # polls -- a re-share changes neither, because attribution
        # stays with the earliest submission
        if song.music_poll_instance_id not in grace_instance_ids
        and song.song_identity not in no_repeat
    ]

    return select_poll_options(candidates, now, rng)


def _closed_and_grace_instance_ids(
    instances: list[MusicPollInstance],
) -> tuple[set[int], set[int]]:
    """Split one poll's instance chain into (closed, grace) ids.

    The chain runs oldest to newest through `next_instance_id`, and
    its tail -- the instance with no successor -- is the still-open
    window. Walking back from the tail marks the two most recent
    closed windows in chain order as the grace period, so the order
    comes from the chain and never from timestamps.
    """
    closed = {i.id for i in instances if i.next_instance_id is not None}
    grace: set[int] = set()
    for tail in (i for i in instances if i.next_instance_id is None):
        current = tail
        for _ in range(GRACE_WINDOW_COUNT):
            predecessors = sorted(
                (i for i in instances if i.next_instance_id == current.id),
                key=lambda i: i.id,
            )
            if not predecessors:
                break
            current = predecessors[0]
            grace.add(current.id)
    return closed, grace


def _load_submissions(
    session: Session, closed_instance_ids: set[int]
) -> list[SongSubmission]:
    """The recorded submissions of the closed windows.

    Each is joined to the Slack message it was recorded from and the
    user who posted it, so the song's date and submitter come from
    the message's original post time and its author -- not from
    when the pickup recorded the row.
    """
    if not closed_instance_ids:
        return []
    responses = session.exec(
        select(MusicPollResponse).where(
            MusicPollResponse.music_poll_instance_id.in_(closed_instance_ids)
        )
    ).all()
    if not responses:
        return []
    messages = {
        m.id: m
        for m in session.exec(
            select(SlackMessage).where(
                SlackMessage.id.in_({r.slack_message_id for r in responses})
            )
        )
    }
    users = {
        u.id: u
        for u in session.exec(
            select(SlackUser).where(
                SlackUser.id.in_({r.slack_user_id for r in responses})
            )
        )
    }
    submissions: list[SongSubmission] = []
    for response in responses:
        message = messages.get(response.slack_message_id)
        user = users.get(response.slack_user_id)
        if message is None or user is None:
            # a response whose message or user row is gone cannot be
            # attributed, so it cannot contribute a candidate
            logger.warning(
                "music poll response %s has no stored message or user; skipping it",
                response.id,
            )
            continue
        submissions.append(
            SongSubmission(
                song_identity=song_identity(response.url),
                song_link=response.url,
                submitter_slack_user_slack_id=user.slack_id,
                submission_date=_as_utc(message.ts),
                music_poll_instance_id=response.music_poll_instance_id,
            )
        )
    return submissions


def _load_run_history(
    session: Session, slack_channel_slack_id: str
) -> tuple[Counter[str], set[str]]:
    """The channel's run-posted poll history.

    Returns how many times each song identity has appeared as an
    option across all of the channel's run-posted polls, and the
    identities featured by the last eight of those polls. Dry runs
    and stale skips record nothing, so they never appear here.
    """
    runs = session.exec(
        select(ScheduledPollRun)
        .where(ScheduledPollRun.slack_channel_slack_id == slack_channel_slack_id)
        .order_by(ScheduledPollRun.run_at.desc(), ScheduledPollRun.id.desc())
    ).all()
    if not runs:
        return Counter(), set()
    options = session.exec(
        select(ScheduledPollRunOption).where(
            ScheduledPollRunOption.scheduled_poll_run_id.in_(
                [run.id for run in runs]
            )
        )
    ).all()
    appearances = Counter(option.song_identity for option in options)
    recent_run_ids = {run.id for run in runs[:NO_REPEAT_POLL_COUNT]}
    no_repeat = {
        option.song_identity
        for option in options
        if option.scheduled_poll_run_id in recent_run_ids
    }
    return appearances, no_repeat


def select_poll_options(
    candidates: list[SongCandidate],
    now: datetime.datetime,
    rng: random.Random,
) -> list[SelectedOption] | None:
    """Pick a channel's poll options from its eligible songs.

    The option count is how many songs can be picked without
    exceeding two options from one submitter: four when four or more
    are pickable, three when exactly three are, and None -- no poll
    this week -- when fewer than three are. Options come from
    distinct submitters whenever enough distinct submitters have
    eligible songs; a submitter appears twice only once distinct
    submitters run out, and that cap wins over the recent/goldie
    slot split, which the slots then fill from each other
    best-effort. Within a slot the fewest-featured songs go first,
    ties broken uniformly at random, so songs newly past the grace
    period surface before anything already featured.
    """
    recent: list[SongCandidate] = []
    goldies: list[SongCandidate] = []
    for song in candidates:
        weeks = (now - song.submission_date) / datetime.timedelta(weeks=1)
        if RECENT_WEEKS_MIN <= weeks <= RECENT_WEEKS_MAX:
            recent.append(song)
        elif weeks > RECENT_WEEKS_MAX:
            goldies.append(song)
        # a song younger than the recent floor sits in neither slot;
        # the grace period keeps that rare, and next week's run ages
        # the song into the recent slot

    by_submitter: dict[str, list[SongCandidate]] = {}
    for song in recent + goldies:
        by_submitter.setdefault(
            song.submitter_slack_user_slack_id, []
        ).append(song)
    pickable = sum(
        min(MAX_OPTIONS_PER_SUBMITTER, len(songs))
        for songs in by_submitter.values()
    )
    if pickable < MIN_OPTION_COUNT:
        return None
    option_count = min(pickable, MAX_OPTION_COUNT)

    # distinct submitters whenever enough exist; a second option
    # from one submitter only once they run out
    per_submitter_limit = (
        1 if len(by_submitter) >= option_count else MAX_OPTIONS_PER_SUBMITTER
    )

    picks: list[str] = []
    counts: Counter[str] = Counter()

    def fill(bucket: list[SongCandidate], target: int) -> int:
        """Take up to `target` songs, fewest appearances first."""
        taken = 0
        for song in sorted(
            bucket, key=lambda s: (s.appearances, rng.random())
        ):
            if taken == target:
                break
            if (
                song.song_identity in picks
                or counts[song.submitter_slack_user_slack_id]
                >= per_submitter_limit
            ):
                continue
            picks.append(song.song_identity)
            counts[song.submitter_slack_user_slack_id] += 1
            taken += 1
        return taken

    recent_taken = fill(recent, RECENT_SLOT_COUNT)
    goldie_taken = fill(goldies, option_count - RECENT_SLOT_COUNT)
    # a slot that came up short borrows from the other, and either
    # slot's leftovers fill what is still missing
    if recent_taken < RECENT_SLOT_COUNT:
        fill(goldies, option_count - len(picks))
        fill(recent, option_count - len(picks))
    else:
        fill(recent, option_count - len(picks))
        fill(goldies, option_count - len(picks))

    by_identity = {song.song_identity: song for song in candidates}
    return [
        SelectedOption(
            song_identity=song.song_identity,
            song_link=song.song_link,
            submitter_slack_user_slack_id=song.submitter_slack_user_slack_id,
            submission_date=song.submission_date,
        )
        for song in (by_identity[identity] for identity in picks)
    ]


def _as_utc(value: datetime.datetime) -> datetime.datetime:
    # slackmessage.ts is stored naive; the selection reads it as UTC
    if value.tzinfo is None:
        return value.replace(tzinfo=datetime.UTC)
    return value.astimezone(datetime.UTC)
