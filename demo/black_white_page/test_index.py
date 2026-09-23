"""Static assertions on index.html's initial-paint background.

Uses only the Python stdlib (re / html.parser) -- there is no browser,
Playwright, Selenium, BeautifulSoup, or lxml available in this repo, so
these tests check the raw markup rather than rendering it.
"""

from __future__ import annotations

import re
from pathlib import Path

_INDEX_HTML = Path(__file__).resolve().parent / "index.html"

_WHITE_BG_RE = re.compile(
    r"background(?:-color)?\s*:\s*(#fff\b|#ffffff\b|white\b)", re.IGNORECASE
)
_HEAD_RE = re.compile(r"<head[^>]*>(.*?)</head>", re.IGNORECASE | re.DOTALL)
_SCRIPT_TAG_RE = re.compile(r"<script[^>]*>", re.IGNORECASE)
_SCRIPT_BLOCK_RE = re.compile(r"<script[^>]*>(.*?)</script>", re.IGNORECASE | re.DOTALL)
_BACKGROUND_ASSIGNMENT_RE = re.compile(
    r"\.style\.background(?:Color)?\s*=|setProperty\(\s*['\"]background", re.IGNORECASE
)

# A click listener attached to document, window, or document.body -- either
# via addEventListener('click', ...) or an onclick assignment.
_CLICK_LISTENER_RE = re.compile(
    r"(?:document\s*\.\s*body|document|window)\s*\.\s*addEventListener\(\s*['\"]click['\"]"
    r"|(?:document\s*\.\s*body|document|window)\s*\.\s*onclick\s*=",
    re.IGNORECASE,
)

# Any background value assigned via `.style.background(Color) = <value>` or
# `setProperty('background(-color)', <value>)`, capturing the value token so
# it can be checked against the two supported colors.
_BACKGROUND_VALUE_RE = re.compile(
    r"\.style\.background(?:Color)?\s*=\s*['\"]([^'\"]+)['\"]"
    r"|setProperty\(\s*['\"]background(?:-color)?['\"]\s*,\s*['\"]([^'\"]+)['\"]",
    re.IGNORECASE,
)

_WHITE_TOKEN_RE = re.compile(r"^(#fff|#ffffff|white)$", re.IGNORECASE)
_BLACK_TOKEN_RE = re.compile(r"^(#000|#000000|black)$", re.IGNORECASE)


def _read_source() -> str:
    return _INDEX_HTML.read_text(encoding="utf-8")


def _top_level_text(script_body: str) -> str:
    """Return script_body with the contents of every {...} block removed.

    What remains is only the statements that execute immediately when the
    script runs -- not the body of any function (e.g. a click handler)
    defined inside it.
    """
    result = []
    depth = 0
    for ch in script_body:
        if ch == "{":
            depth += 1
            continue
        if ch == "}":
            depth = max(0, depth - 1)
            continue
        if depth == 0:
            result.append(ch)
    return "".join(result)


def _click_handler_span(script_body: str) -> tuple[int, int]:
    """Return the (open_brace_index, close_brace_index) of the function body
    passed to the click listener registration found in script_body."""
    listener_match = _CLICK_LISTENER_RE.search(script_body)
    assert listener_match, "no click listener registration found in <script>"

    open_brace = script_body.index("{", listener_match.end())
    depth = 0
    for i in range(open_brace, len(script_body)):
        if script_body[i] == "{":
            depth += 1
        elif script_body[i] == "}":
            depth -= 1
            if depth == 0:
                return open_brace, i
    raise AssertionError("unbalanced braces in click handler function body")


def test_index_html_exists_and_nonempty():
    assert _INDEX_HTML.is_file(), f"{_INDEX_HTML} does not exist"
    assert _INDEX_HTML.stat().st_size > 0, f"{_INDEX_HTML} is empty"


def test_white_background_declared_in_head():
    source = _read_source()
    head_match = _HEAD_RE.search(source)
    assert head_match, "no <head>...</head> block found"
    head_content = head_match.group(1)
    assert _WHITE_BG_RE.search(head_content), (
        "no white background declaration (#fff / #ffffff / white) found "
        "inside <head>, in a <style> block or inline style attribute"
    )


def test_white_background_precedes_first_script_tag():
    source = _read_source()
    white_match = _WHITE_BG_RE.search(source)
    assert white_match, "no white background declaration found in source"

    script_match = _SCRIPT_TAG_RE.search(source)
    if script_match is None:
        # No <script> tag at all -- the white declaration trivially precedes it.
        return

    assert white_match.start() < script_match.start(), (
        "white background declaration must appear before the first "
        "<script> tag in source order"
    )


def test_no_script_assigns_initial_background():
    """No background assignment runs at script top level (i.e. at load
    time). A background assignment nested inside a function body -- such as
    a click handler, which only runs on a later user click -- is fine and
    does not affect the initial paint."""
    source = _read_source()
    for script_match in _SCRIPT_BLOCK_RE.finditer(source):
        script_body = script_match.group(1)
        top_level = _top_level_text(script_body)
        assert not _BACKGROUND_ASSIGNMENT_RE.search(top_level), (
            "a <script> block assigns the background at its top level -- "
            "the initial background must come from markup/CSS, not "
            "JavaScript that runs at load time"
        )


def test_click_listener_registered_on_document_or_body():
    source = _read_source()
    assert _CLICK_LISTENER_RE.search(source), (
        "no click listener (addEventListener('click', ...) or onclick=) "
        "found registered on document, window, or document.body"
    )


def test_exactly_two_background_colors_used():
    """Every background value in the page -- in <style> declarations and in
    script assignments alike -- must be one of the two supported colors
    (white or black); no third color string is allowed."""
    source = _read_source()
    combined_re = re.compile(
        r"background(?:-color)?\s*:\s*['\"]?([#A-Za-z0-9]+)"
        r"|\.style\.background(?:Color)?\s*=\s*['\"]([^'\"]+)['\"]"
        r"|setProperty\(\s*['\"]background(?:-color)?['\"]\s*,\s*['\"]([^'\"]+)['\"]",
        re.IGNORECASE,
    )
    groups_seen = set()
    for match in combined_re.finditer(source):
        token = next(g for g in match.groups() if g is not None).strip()
        if _WHITE_TOKEN_RE.match(token):
            groups_seen.add("white")
        elif _BLACK_TOKEN_RE.match(token):
            groups_seen.add("black")
        else:
            raise AssertionError(
                f"unexpected background color value {token!r} -- only "
                "white (#fff/#ffffff/white) and black (#000/#000000/black) "
                "are supported, no third state"
            )
    assert groups_seen == {"white", "black"}, (
        f"expected both white and black background values to appear, "
        f"found: {groups_seen or 'none'}"
    )


def test_background_mutation_is_inside_click_handler():
    """Every JS-style background assignment (.style.background(Color) = /
    setProperty('background...', ...)) must be lexically inside the click
    handler's function body, not before or after it."""
    source = _read_source()
    for script_match in _SCRIPT_BLOCK_RE.finditer(source):
        script_body = script_match.group(1)
        value_matches = list(_BACKGROUND_VALUE_RE.finditer(script_body))
        if not value_matches:
            continue
        open_brace, close_brace = _click_handler_span(script_body)
        for value_match in value_matches:
            assert open_brace < value_match.start() < close_brace, (
                "a background assignment falls outside the click handler's "
                "function body -- every background mutation must be "
                "lexically inside the click handler"
            )
