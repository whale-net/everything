"""Slack model DAL functions."""

import logging
from typing import Optional

from sqlmodel import Session, and_, or_, select

from friendly_computing_machine.db.util import SessionManager, db_update
from friendly_computing_machine.models.music_poll import MusicPoll, MusicPollInstance
from friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackCommand,
    SlackCommandCreate,
    SlackMessage,
    SlackMessageCreate,
    SlackSpecialChannel,
    SlackSpecialChannelType,
    SlackTeam,
    SlackTeamCreate,
    SlackUser,
    SlackUserCreate,
)

logger = logging.getLogger(__name__)


def get_slack_message_from_id(
    message_id: int, session: Optional[Session] = None
) -> SlackMessage | None:
    """Get a Slack message by its database ID."""
    with SessionManager(session) as session:
        return session.get(SlackMessage, message_id)


def get_music_poll_channel_slack_ids() -> set[str]:
    """Get Slack channel IDs that have music polls."""
    stmt = select(SlackChannel.slack_id).join(
        MusicPoll, MusicPoll.slack_channel_id == SlackChannel.id
    )
    with SessionManager() as session:
        results = session.exec(stmt)
    return {row for row in results}


def get_bot_slack_user_slack_ids() -> set[str]:
    """Get Slack IDs of bot users."""
    stmt = select(SlackUser.slack_id).where(SlackUser.is_bot)
    with SessionManager() as session:
        results = session.exec(stmt)
    return {row for row in results}


def insert_message(in_message: SlackMessageCreate) -> SlackMessage:
    """Insert a new Slack message."""
    message = SlackMessage(
        slack_id=in_message.slack_id,
        slack_team_slack_id=in_message.slack_team_slack_id,
        slack_channel_slack_id=in_message.slack_channel_slack_id,
        slack_user_slack_id=in_message.slack_user_slack_id,
        text=in_message.text,
        ts=in_message.ts,
        thread_ts=in_message.thread_ts,
        parent_user_slack_id=in_message.parent_user_slack_id,
    )
    with SessionManager() as session:
        session.add(message)
        session.commit()
        # TODO - does return after refresh cause any issues?
        # I remember this being problematic at some point but maybe I was doing something wrong then
        session.refresh(message)
    return message


def upsert_message(slack_message: SlackMessageCreate) -> SlackMessage:
    """Insert or update a Slack message."""
    with SessionManager() as session:
        # check if exists and insert. very non-optimal, especially if rbar, but small table so no prob
        # TODO - covering index? this may be an expensive query

        # by_id
        condition_by_id = and_(
            SlackMessage.slack_id == slack_message.slack_id,
            SlackMessage.slack_id.is_not(None),
        )
        # by_ts
        condition_by_ts = and_(
            SlackMessage.slack_id.is_(None),
            # unique on ts and channel
            # channel unique on team and channel
            SlackMessage.slack_team_slack_id == slack_message.slack_team_slack_id,
            SlackMessage.slack_channel_slack_id == slack_message.slack_channel_slack_id,
            SlackMessage.ts == slack_message.ts,
        )

        stmt = select(SlackMessage).where(
            or_(
                condition_by_id,
                condition_by_ts,
            )
        )
        result = session.exec(stmt).one_or_none()
        if result is None:
            result = insert_message(slack_message)
        else:
            # TODO - make generic, I think I can probably reuse another function
            # do we need a model class for each variant? do we even need creates?
            update_dict = slack_message.model_dump(exclude_unset=True)
            result = db_update(session, SlackMessage, result.id, update_dict)

    return result


def select_distinct_slack_team_slack_id_from_slack_message() -> set[str]:
    """Get distinct Slack team IDs from messages."""
    with SessionManager() as session:
        # TODO - this will table scan, no index. need to watch out for performance at some far future point
        stmt = select(SlackMessage.slack_team_slack_id).distinct()
        results = session.exec(stmt).all()
    return {row for row in results}


def upsert_slack_teams(slack_teams: list[SlackTeamCreate]) -> list[SlackTeam]:
    """Insert or update multiple Slack teams."""
    with SessionManager() as session:
        out_teams = []
        for slack_team in slack_teams:
            out_teams.append(upsert_slack_team(slack_team, session))
    return out_teams


def upsert_slack_team(
    slack_team: SlackTeamCreate, session: Optional[Session] = None
) -> SlackTeam:
    """Insert or update a Slack team."""
    with SessionManager(session) as session:
        # check if exists and insert. very non-optimal, especially if rbar, but small table so no prob
        stmt = select(SlackTeam).where(SlackTeam.slack_id == slack_team.slack_id)
        result = session.exec(stmt).one_or_none()
        if result is None:
            result = slack_team.to_slack_team()
            session.add(result)
            session.commit()
            session.refresh(result)

    return result


def get_slack_teams(session: Optional[Session] = None) -> list[SlackTeam]:
    """Get all Slack teams."""
    with SessionManager(session) as session:
        stmt = select(SlackTeam)
        return list(session.exec(stmt).all())


def get_slack_team_id_map(session: Optional[Session] = None) -> dict[str, int]:
    """Get mapping of Slack team ID to database ID."""
    with SessionManager(session) as session:
        slack_teams = get_slack_teams(session)
    return {slack_team.slack_id: slack_team.id for slack_team in slack_teams}


def get_user_teams_from_messages(slack_team_slack_id: str) -> set[tuple[str, str]]:
    """Get user-team combinations from messages for a specific team."""
    with SessionManager() as session:
        # TODO - this will table scan, no index. need to watch out for performance at some far future point
        stmt = (
            select(SlackMessage.slack_user_slack_id, SlackMessage.slack_team_slack_id)
            .where(SlackMessage.slack_team_slack_id == slack_team_slack_id)
            .distinct()
        )
        results = session.exec(stmt).all()
    return {(row.slack_user_slack_id, row.slack_team_slack_id) for row in results}


def upsert_slack_users(slack_users: list[SlackUserCreate]) -> list[SlackUser]:
    """Insert or update multiple Slack users."""
    out_users = []
    with SessionManager() as session:
        slack_team_id_map = get_slack_team_id_map(session)
        for slack_user in slack_users:
            out_users.append(
                upsert_slack_users_activity(
                    slack_user, session=session, slack_team_id_map=slack_team_id_map
                )
            )
    return out_users


def upsert_slack_users_activity(
    slack_user: SlackUserCreate,
    session: Optional[Session] = None,
    slack_team_id_map: Optional[dict[str, int]] = None,
) -> SlackUser:
    """Insert or update a Slack user with activity tracking."""
    # TODO this code sucks, should rewrite

    with SessionManager(session) as session:
        if slack_team_id_map is None:
            slack_team_id_map = get_slack_team_id_map(session)
        slack_team_id = slack_team_id_map.get(slack_user.slack_team_slack_id)

        # check if exists. very non-optimal, especially if rbar, but small table so no prob
        stmt = select(SlackUser).where(SlackUser.slack_id == slack_user.slack_id)
        result = session.exec(stmt).one_or_none()
        if result is None:
            user = slack_user.to_slack_user(slack_team_id)
            session.add(user)
            session.commit()
            session.refresh(user)
            result = user
        elif (
            result.name == slack_user.name
            # these aren't expected to change but keeping for consistency
            or result.slack_team_slack_id != slack_user.slack_team_slack_id
            or result.slack_team_id != slack_team_id
        ):
            # TODO - pydantic idiomatic way of doing this
            result.name = slack_user.name
            result.slack_team_slack_id = slack_user.slack_team_slack_id
            result.slack_team_id = slack_team_id

            session.add(result)
            session.commit()
            session.refresh(result)

    return result


def get_slack_channel(
    slack_channel_id: int | None = None,
    slack_channel_slack_id: str | None = None,
    session: Optional[Session] = None,
) -> SlackChannel | None:
    """
    Get a SlackChannel by either slack_channel_id or slack_channel_slack_id.

    :param slack_channel_id: database id
    :param slack_channel_slack_id: slack id
    :param session:
    :return:
    """
    if slack_channel_id is None and slack_channel_slack_id is None:
        raise ValueError(
            "Either slack_channel_id or slack_channel_slack_id must be provided"
        )
    if slack_channel_id is not None and slack_channel_slack_id is not None:
        raise ValueError(
            "Only one of slack_channel_id or slack_channel_slack_id must be provided"
        )

    with SessionManager(session) as session:
        stmt = select(SlackChannel)
        if slack_channel_id is not None:
            stmt = stmt.where(SlackChannel.id == slack_channel_id)
        elif slack_channel_slack_id is not None:
            stmt = stmt.where(SlackChannel.slack_id == slack_channel_slack_id)
        return session.exec(stmt).one_or_none()


def find_poll_instance_messages(poll_instance: MusicPollInstance) -> list[SlackMessage]:
    """Find messages for a specific music poll instance."""
    with SessionManager() as session:
        # this will return the next poll, so if there is ever some graph fuckery this
        # should hopefully prevent corruption by at least being deterministic
        # although some messages will be dropped
        # still a very edge case situation, that should be redesigned
        next_instance_subquery = (
            select(MusicPollInstance.created_at)
            .where(MusicPollInstance.id == poll_instance.next_instance_id)
            .order_by(MusicPollInstance.created_at)
            .limit(1)
            .scalar_subquery()
        )
        # TODO - should just join it in, but this will work I suppose
        slack_channel_id_subquery = (
            select(MusicPoll.slack_channel_id)
            .where(MusicPoll.id == poll_instance.music_poll_id)
            .scalar_subquery()
        )
        stmt = select(SlackMessage).where(
            and_(
                SlackMessage.slack_channel_id == slack_channel_id_subquery,
                SlackMessage.ts >= poll_instance.created_at,
                SlackMessage.ts < next_instance_subquery,
            )
        )
        return list(session.exec(stmt).all())


def insert_slack_command(
    slack_command: SlackCommandCreate, session: Optional[Session] = None
) -> SlackCommand:
    """Insert a new Slack command."""
    with SessionManager(session) as session:
        db_slack_command = SlackCommand.from_slack_command_create(slack_command)
        session.add(db_slack_command)
        session.commit()
        session.refresh(db_slack_command)
        return db_slack_command


def get_slack_command_by_id(
    slack_command_id: int, session: Optional[Session] = None
) -> SlackCommand | None:
    """Get a Slack command by ID."""
    with SessionManager(session) as session:
        return session.get(SlackCommand, slack_command_id)


def update_slack_command(
    slack_command_id: int, updates: dict, session: Optional[Session] = None
) -> SlackCommand | None:
    """Update a Slack command."""
    with SessionManager(session) as session:
        slack_command = db_update(session, SlackCommand, slack_command_id, updates)
    return slack_command


def get_slack_special_channel_type_from_name(
    name: str,
    session: Optional[Session] = None,
) -> SlackSpecialChannelType | None:
    """Get a Slack special channel type by name."""
    with SessionManager(session) as session:
        stmt = select(SlackSpecialChannelType).where(
            SlackSpecialChannelType.type_name == name
        )
        return session.exec(stmt).one_or_none()


def get_slack_special_channels_from_type(
    slack_special_channel_type: SlackSpecialChannelType,
    session: Optional[Session] = None,
) -> list[tuple[SlackSpecialChannel, SlackChannel, SlackSpecialChannelType]] | None:
    """Get all Slack special channels of a specific type."""
    with SessionManager(session) as session:
        stmt = select(SlackSpecialChannel).where(
            SlackSpecialChannel.slack_special_channel_type_id
            == slack_special_channel_type.id
        )
        results = session.exec(stmt).all()
        return [
            (result, result.slack_channel, result.slack_special_channel_type)
            for result in results
        ]
