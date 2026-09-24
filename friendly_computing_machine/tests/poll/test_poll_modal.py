import pytest

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.modal import (
    LIMIT_BLOCK,
    OPTION_BLOCK_PREFIX,
    POLL_ADD_OPTION_ACTION,
    QUESTION_BLOCK,
    SETTINGS_BLOCK,
    PollModalError,
    PollModalMetadata,
    build_poll_modal,
    parse_poll_modal,
)


def _view(
    question="Lunch?", options=("Tacos", "Pizza", ""), anonymous=False, limit=None
):
    values = {
        QUESTION_BLOCK: {"value": {"type": "plain_text_input", "value": question}}
    }
    for i, opt in enumerate(options):
        values[f"{OPTION_BLOCK_PREFIX}{i}"] = {
            "value": {"type": "plain_text_input", "value": opt or None}
        }
    values[SETTINGS_BLOCK] = {
        "value": {"selected_options": [{"value": "anonymous"}] if anonymous else []}
    }
    values[LIMIT_BLOCK] = {
        "value": {"selected_option": {"value": str(limit) if limit else "unlimited"}}
    }
    return {"state": {"values": values}}


def _block_ids(view):
    return [b.get("block_id") for b in view["blocks"]]


def test_modal_round_trips_metadata():
    view = build_poll_modal(PollModalMetadata(channel_id="C1"))
    assert PollModalMetadata.loads(view["private_metadata"]) == PollModalMetadata(
        "C1", 3
    )
    option_blocks = [
        b
        for b in view["blocks"]
        if b.get("block_id", "").startswith(OPTION_BLOCK_PREFIX)
    ]
    assert [b["optional"] for b in option_blocks] == [False, False, True]


def test_add_option_button_hidden_at_max():
    def has_add(view):
        return any(
            e.get("action_id") == POLL_ADD_OPTION_ACTION
            for b in view["blocks"]
            for e in b.get("elements", [])
        )

    assert has_add(build_poll_modal(PollModalMetadata("C1", 9)))
    full = build_poll_modal(PollModalMetadata("C1", 10))
    assert not has_add(full)
    assert f"{OPTION_BLOCK_PREFIX}9" in _block_ids(full)


def test_parse_submission():
    spec = parse_poll_modal(
        _view(options=("Tacos", "", "Salad"), anonymous=True, limit=1)
    )
    assert spec.question == "Lunch?"
    assert spec.options == ["Tacos", "Salad"]
    assert spec.anonymous
    assert spec.vote_limit == 1


def test_parse_orders_options_numerically():
    opts = [f"o{i}" for i in range(10)]
    assert parse_poll_modal(_view(options=opts)).options == opts


def test_limit_covering_all_options_is_unlimited():
    assert parse_poll_modal(_view(limit=2)).vote_limit is None


@pytest.mark.parametrize(
    "view, block",
    [
        (_view(options=("Tacos", "", "")), f"{OPTION_BLOCK_PREFIX}0"),
        (_view(question="   "), QUESTION_BLOCK),
    ],
)
def test_errors_are_keyed_by_block(view, block):
    with pytest.raises(PollModalError) as exc:
        parse_poll_modal(view)
    assert list(exc.value.errors) == [block]
