"""Parse `/poll` command text in the Simple Poll style.

/poll "Question?" "Option A" "Option B" [anonymous] [limit N]
"""

from dataclasses import dataclass

MIN_OPTIONS = 2
MAX_OPTIONS = 10
MAX_QUESTION_LEN = 300
MAX_OPTION_LEN = 200

# Slack clients frequently auto-convert straight quotes to curly ones
_OPEN_QUOTES = {'"', "“", "„"}
_CLOSE_QUOTES = {'"', "”", "“"}

USAGE = (
    'Usage: `/poll "Question?" "Option 1" "Option 2" [anonymous] [limit N]`\n'
    "Wrap the question and each option in double quotes. "
    f"{MIN_OPTIONS}-{MAX_OPTIONS} options. "
    "`anonymous` hides who voted; `limit N` caps votes per person."
)


class PollParseError(ValueError):
    pass


@dataclass(frozen=True)
class PollSpec:
    question: str
    options: list[str]
    anonymous: bool = False
    # None = unlimited
    vote_limit: int | None = None


def _tokenize(text: str) -> list[tuple[str, bool]]:
    """Split into (token, was_quoted) pairs; only double quotes group words."""
    tokens: list[tuple[str, bool]] = []
    i, n = 0, len(text)
    while i < n:
        ch = text[i]
        if ch.isspace():
            i += 1
            continue
        if ch in _OPEN_QUOTES:
            end = i + 1
            while end < n and text[end] not in _CLOSE_QUOTES:
                end += 1
            if end >= n:
                raise PollParseError("Unclosed quote.")
            tokens.append((text[i + 1 : end].strip(), True))
            i = end + 1
            continue
        end = i
        while end < n and not text[end].isspace() and text[end] not in _OPEN_QUOTES:
            end += 1
        tokens.append((text[i:end], False))
        i = end
    return tokens


def parse_poll_command(text: str) -> PollSpec:
    tokens = _tokenize(text or "")
    quoted: list[str] = []
    anonymous = False
    vote_limit: int | None = None

    idx = 0
    while idx < len(tokens):
        value, was_quoted = tokens[idx]
        idx += 1
        if was_quoted:
            if not value:
                raise PollParseError("Question and options cannot be empty.")
            quoted.append(value)
            continue
        keyword = value.lower()
        if keyword == "anonymous":
            anonymous = True
        elif keyword == "limit":
            if idx >= len(tokens) or tokens[idx][1] or not tokens[idx][0].isdigit():
                raise PollParseError("`limit` must be followed by a number.")
            vote_limit = int(tokens[idx][0])
            idx += 1
            if vote_limit < 1:
                raise PollParseError("`limit` must be at least 1.")
        else:
            raise PollParseError(
                f"Unexpected `{value}`. Wrap the question and options in double quotes."
            )

    if not quoted:
        raise PollParseError("A poll needs a question.")
    question, options = quoted[0], quoted[1:]
    if len(options) < MIN_OPTIONS:
        raise PollParseError(f"A poll needs at least {MIN_OPTIONS} options.")
    if len(options) > MAX_OPTIONS:
        raise PollParseError(f"A poll can have at most {MAX_OPTIONS} options.")
    if len(question) > MAX_QUESTION_LEN:
        raise PollParseError(f"Question is longer than {MAX_QUESTION_LEN} characters.")
    if any(len(o) > MAX_OPTION_LEN for o in options):
        raise PollParseError(f"Options must be at most {MAX_OPTION_LEN} characters.")

    # a limit that covers every option is no limit at all
    if vote_limit is not None and vote_limit >= len(options):
        vote_limit = None

    return PollSpec(
        question=question,
        options=options,
        anonymous=anonymous,
        vote_limit=vote_limit,
    )
