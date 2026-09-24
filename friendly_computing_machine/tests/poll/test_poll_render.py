import datetime

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.render import (
    POLL_CLOSE_ACTION,
    decode_vote_value,
    render_poll_blocks,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    PollOptionTally,
    PollSnapshot,
)
from friendly_computing_machine.src.friendly_computing_machine.models.poll import (
    Poll,
    PollOption,
)


def _snapshot(**poll_kwargs) -> PollSnapshot:
    poll = Poll(
        id=7,
        question="Lunch?",
        slack_channel_slack_id="C1",
        creator_slack_user_slack_id="U0",
        **poll_kwargs,
    )
    return PollSnapshot(
        poll=poll,
        options=[
            PollOptionTally(
                PollOption(id=70, poll_id=7, position=0, text="Tacos"), ["U1", "U2"]
            ),
            PollOptionTally(PollOption(id=71, poll_id=7, position=1, text="Pizza"), []),
        ],
    )


def _texts(blocks):
    out = []
    for b in blocks:
        if "text" in b:
            out.append(b["text"]["text"])
        for e in b.get("elements", []):
            if e.get("type") == "mrkdwn":
                out.append(e["text"])
    return out


def test_open_poll_has_vote_buttons_and_voters():
    blocks = render_poll_blocks(_snapshot())
    buttons = [b["accessory"] for b in blocks if "accessory" in b]
    assert [decode_vote_value(b["value"]) for b in buttons] == [(7, 70), (7, 71)]
    assert len({b["action_id"] for b in buttons}) == 2
    texts = _texts(blocks)
    assert "<@U1>, <@U2>" in texts
    assert any("2 votes" in t for t in texts)
    assert any("Created by <@U0> · created with /wpoll" in t for t in texts)
    assert blocks[-1]["elements"][0]["action_id"] == POLL_CLOSE_ACTION


def test_anonymous_hides_voters():
    texts = _texts(render_poll_blocks(_snapshot(anonymous=True)))
    assert not any("<@U1>" in t for t in texts)
    assert any("anonymous" in t for t in texts)


def test_closed_poll_has_no_buttons():
    blocks = render_poll_blocks(
        _snapshot(closed_at=datetime.datetime.now(datetime.UTC))
    )
    assert not any("accessory" in b for b in blocks)
    assert not any(b["type"] == "actions" for b in blocks)
    assert any("*closed*" in t for t in _texts(blocks))
