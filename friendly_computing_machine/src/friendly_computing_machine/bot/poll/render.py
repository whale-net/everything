"""Block Kit rendering for `/wpoll` messages."""

from friendly_computing_machine.src.friendly_computing_machine.db.dal.poll_dal import (
    PollSnapshot,
)

POLL_VOTE_ACTION_PREFIX = "poll_vote_"
POLL_CLOSE_ACTION = "poll_close"

_NUMBER_EMOJI = [
    ":one:",
    ":two:",
    ":three:",
    ":four:",
    ":five:",
    ":six:",
    ":seven:",
    ":eight:",
    ":nine:",
    ":keycap_ten:",
]


def encode_vote_value(poll_id: int, poll_option_id: int) -> str:
    return f"{poll_id}:{poll_option_id}"


def decode_vote_value(value: str) -> tuple[int, int]:
    poll_id, poll_option_id = value.split(":", 1)
    return int(poll_id), int(poll_option_id)


def _plural(n: int, word: str) -> str:
    return f"{n} {word}" if n == 1 else f"{n} {word}s"


def render_poll_text(snapshot: PollSnapshot) -> str:
    """Plain-text fallback used for notifications and screen readers."""
    return f"Poll: {snapshot.poll.question}"


def render_poll_blocks(snapshot: PollSnapshot) -> list[dict]:
    poll = snapshot.poll
    is_closed = poll.closed_at is not None
    blocks: list[dict] = [
        {
            "type": "section",
            "text": {"type": "mrkdwn", "text": f"*{poll.question}*"},
        }
    ]

    for idx, tally in enumerate(snapshot.options):
        count = len(tally.voters)
        section: dict = {
            "type": "section",
            "text": {
                "type": "mrkdwn",
                "text": f"{_NUMBER_EMOJI[idx]} {tally.option.text}  `{count}`",
            },
        }
        if not is_closed:
            section["accessory"] = {
                "type": "button",
                "action_id": f"{POLL_VOTE_ACTION_PREFIX}{tally.option.id}",
                "text": {
                    "type": "plain_text",
                    "text": _NUMBER_EMOJI[idx],
                    "emoji": True,
                },
                "value": encode_vote_value(poll.id, tally.option.id),
            }
        blocks.append(section)
        if count and not poll.anonymous:
            blocks.append(
                {
                    "type": "context",
                    "elements": [
                        {
                            "type": "mrkdwn",
                            "text": ", ".join(f"<@{u}>" for u in tally.voters),
                        }
                    ],
                }
            )

    footer = [
        f"Created by <@{poll.creator_slack_user_slack_id}>",
        "created with /wpoll",
    ]
    if poll.anonymous:
        footer.append("anonymous")
    if poll.vote_limit is not None:
        footer.append(f"{_plural(poll.vote_limit, 'vote')} per person")
    footer.append(_plural(snapshot.total_votes, "vote"))
    if is_closed:
        footer.append("*closed*")
    blocks.append(
        {
            "type": "context",
            "elements": [{"type": "mrkdwn", "text": " · ".join(footer)}],
        }
    )

    if not is_closed:
        blocks.append(
            {
                "type": "actions",
                "elements": [
                    {
                        "type": "button",
                        "action_id": POLL_CLOSE_ACTION,
                        "text": {"type": "plain_text", "text": "Close poll"},
                        "value": str(poll.id),
                        "confirm": {
                            "title": {"type": "plain_text", "text": "Close poll?"},
                            "text": {
                                "type": "plain_text",
                                "text": "Nobody will be able to vote after it closes.",
                            },
                            "confirm": {"type": "plain_text", "text": "Close"},
                            "deny": {"type": "plain_text", "text": "Cancel"},
                        },
                    }
                ],
            }
        )
    return blocks
