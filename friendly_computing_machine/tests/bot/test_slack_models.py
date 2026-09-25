"""Slack model utilities and the retained route-table models.

The manman V1 status-block renderers are gone with V1, but the
`slackspecialchanneltype` / `slackspecialchannel` models they took a
`SlackSpecialChannelType` from are retained for the next milestone's routing
work. Their zero call sites are the intended state, so this file keeps them
live by driving the type-to-channel lookup against a real session.

The migration-bound liveness gate for these tables is
`//friendly_computing_machine/tests:test_route_table`; this is the cheap
unit-level exerciser, not a replacement for it.
"""

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine

from friendly_computing_machine.src.friendly_computing_machine.bot.slack_models import (
    render_blocks_to_text,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.slack_dal import (
    get_slack_special_channel_type_from_name,
    get_slack_special_channels_from_type,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
    SlackSpecialChannel,
    SlackSpecialChannelType,
)

ROUTE_MODELS = (SlackChannel, SlackSpecialChannelType, SlackSpecialChannel)


@pytest.fixture
def session():
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach_schema(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=[m.__table__ for m in ROUTE_MODELS])
    with Session(engine) as s:
        yield s


def test_render_blocks_to_text_is_non_empty_for_a_section():
    from slack_sdk.models.blocks import SectionBlock

    text = render_blocks_to_text([SectionBlock(text={"type": "mrkdwn", "text": "hi"})])

    assert text == "hi"


def test_route_type_lookup_returns_nothing_when_unconfigured(session):
    """An empty route table is a healthy deployment, not a defect."""
    assert get_slack_special_channel_type_from_name("anything", session=session) is None


def test_route_type_lookup_resolves_its_own_channels(session):
    channel_type = SlackSpecialChannelType(
        type_name="some_type", friendly_type_name="Some Type"
    )
    session.add(channel_type)
    session.commit()

    channel = SlackChannel(
        slack_id="C_ROUTE", name="routed", channel_type="public_channel"
    )
    session.add(channel)
    session.commit()
    session.refresh(channel)
    session.add(
        SlackSpecialChannel(
            slack_channel_id=channel.id,
            slack_special_channel_type_id=channel_type.id,
            reason="routed",
        )
    )
    session.commit()

    found = get_slack_special_channel_type_from_name("some_type", session=session)
    assert found is not None
    assert found.id == channel_type.id

    routes = get_slack_special_channels_from_type(found, session=session)
    assert [route.slack_channel.slack_id for route, _, _ in routes] == ["C_ROUTE"]
