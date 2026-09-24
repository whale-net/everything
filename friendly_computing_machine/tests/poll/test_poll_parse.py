import pytest

from friendly_computing_machine.src.friendly_computing_machine.bot.poll.parse import (
    PollParseError,
    parse_poll_command,
)


def test_basic_poll():
    spec = parse_poll_command('"Lunch?" "Tacos" "Pizza"')
    assert spec.question == "Lunch?"
    assert spec.options == ["Tacos", "Pizza"]
    assert not spec.anonymous
    assert spec.vote_limit is None


def test_curly_quotes_and_apostrophes():
    spec = parse_poll_command("“What's up?” “not much” “it's fine”")
    assert spec.question == "What's up?"
    assert spec.options == ["not much", "it's fine"]


def test_flags_anywhere_case_insensitive():
    spec = parse_poll_command('Anonymous "Q" "a" "b" "c" LIMIT 1')
    assert spec.anonymous
    assert spec.vote_limit == 1


def test_limit_covering_all_options_is_unlimited():
    assert parse_poll_command('"Q" "a" "b" limit 2').vote_limit is None


def test_quoted_keyword_is_an_option():
    spec = parse_poll_command('"Q" "anonymous" "limit"')
    assert spec.options == ["anonymous", "limit"]
    assert not spec.anonymous


@pytest.mark.parametrize(
    "text",
    [
        "",
        '"Q"',
        '"Q" "only one"',
        '"Q" "a" "b',
        "Q a b",
        '"Q" "a" "b" limit',
        '"Q" "a" "b" limit 0',
        '"Q" "a" "b" limit "2"',
        '"Q" "a" ""',
        '"Q" ' + " ".join(f'"{i}"' for i in range(11)),
    ],
)
def test_invalid(text):
    with pytest.raises(PollParseError):
        parse_poll_command(text)
