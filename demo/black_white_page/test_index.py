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

# The handler argument/value of a click listener registration -- either the
# literal keyword `function` (an inline function expression) or an
# identifier (a reference to a separately-defined named function, e.g. a
# shared toggle function also reusable by future keyboard handling).
_HANDLER_REF_RE = re.compile(
    r"(?:addEventListener\(\s*['\"]click['\"]\s*,\s*|\.onclick\s*=\s*)"
    r"(function\b|[A-Za-z_$][\w$]*)",
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


def _brace_span_from(text: str, open_brace: int) -> tuple[int, int]:
    """Return (open_brace, close_brace) for the brace-delimited block that
    starts at open_brace (text[open_brace] must be '{')."""
    depth = 0
    for i in range(open_brace, len(text)):
        if text[i] == "{":
            depth += 1
        elif text[i] == "}":
            depth -= 1
            if depth == 0:
                return open_brace, i
    raise AssertionError("unbalanced braces")


def _click_handler_span(script_body: str) -> tuple[int, int]:
    """Return the (open_brace_index, close_brace_index) of the click
    handler's function body -- either an inline function literal passed
    directly to the listener registration, or the body of a named function
    the registration refers to by reference (e.g. a shared toggle function
    also reusable by future keyboard handling)."""
    assert _CLICK_LISTENER_RE.search(script_body), (
        "no click listener registration found in <script>"
    )
    ref_match = _HANDLER_REF_RE.search(script_body)
    assert ref_match, "no click listener handler argument found in <script>"

    token = ref_match.group(1)
    if token.lower() == "function":
        open_brace = script_body.index("{", ref_match.end())
        return _brace_span_from(script_body, open_brace)

    # Named handler reference -- follow it to its own function declaration.
    decl_re = re.compile(r"function\s+" + re.escape(token) + r"\s*\([^)]*\)\s*\{")
    decl_match = decl_re.search(script_body)
    assert decl_match, (
        f"click handler references {token!r} but no function {token}(...) "
        "definition was found"
    )
    open_brace = decl_match.end() - 1
    return _brace_span_from(script_body, open_brace)


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


def test_storage_key_constant_used_by_setitem():
    source = _read_source()
    assert re.search(r"\bSTORAGE_KEY\s*=\s*['\"][^'\"]+['\"]", source), (
        "no STORAGE_KEY constant declaration found"
    )
    assert re.search(
        r"localStorage\.setItem\(\s*STORAGE_KEY\s*,", source, re.IGNORECASE
    ), "localStorage.setItem must be called with the STORAGE_KEY constant"


_JS_LINE_COMMENT_RE = re.compile(r"//[^\n]*")
_JS_BLOCK_COMMENT_RE = re.compile(r"/\*.*?\*/", re.DOTALL)


def _strip_js_comments(text: str) -> str:
    """Blank out // and /* */ comments (replacing each with same-length
    whitespace, preserving every other character's offset) so prose
    mentioning a keyword (e.g. a comment that says "can throw") doesn't
    masquerade as real code."""

    def _blank(match: re.Match[str]) -> str:
        return "".join(" " if c != "\n" else "\n" for c in match.group(0))

    return _JS_BLOCK_COMMENT_RE.sub(_blank, _JS_LINE_COMMENT_RE.sub(_blank, text))


def _try_catch_spans(source: str) -> list[tuple[int, int, str]]:
    """Return (try_open_brace, try_close_brace, catch_body) for every
    try {...} catch (...) {...} block in source."""
    spans = []
    for try_match in re.finditer(r"try\s*\{", source):
        try_open, try_close = _brace_span_from(source, try_match.end() - 1)
        rest = source[try_close + 1 :]
        catch_match = re.match(r"\s*catch\s*\([^)]*\)\s*\{", rest)
        assert catch_match, "a try block has no matching catch block"
        catch_open, catch_close = _brace_span_from(
            source, try_close + 1 + catch_match.end() - 1
        )
        spans.append((try_open, try_close, source[catch_open + 1 : catch_close]))
    return spans


def test_localstorage_access_is_inside_non_rethrowing_try():
    """Every localStorage access -- both the setItem call and the bare
    `localStorage` property reference, which can itself throw
    (SecurityError) when storage is disabled -- must be lexically inside a
    try block, and that try's catch must not rethrow."""
    source = _read_source()
    spans = _try_catch_spans(source)
    assert spans, "no try/catch block found wrapping localStorage access"
    for _, _, catch_body in spans:
        assert not re.search(r"\bthrow\b", _strip_js_comments(catch_body)), (
            "a catch block wrapping localStorage access rethrows -- a "
            "persistence failure must never propagate out"
        )

    code_only = _strip_js_comments(source)
    occurrences = [m.start() for m in re.finditer(r"\blocalStorage\b", code_only)]
    assert occurrences, "no localStorage access found"
    for idx in occurrences:
        assert any(open_i < idx < close_i for open_i, close_i, _ in spans), (
            f"localStorage access at source offset {idx} is not lexically "
            "inside a try block"
        )


def test_applytoggle_applies_display_before_saving():
    """Inside the toggle function, the background assignment must precede
    the persistence call in source order -- the visible change must never
    wait on or be reverted by the write."""
    source = _read_source()
    for script_match in _SCRIPT_BLOCK_RE.finditer(source):
        script_body = script_match.group(1)
        decl_match = re.search(
            r"function\s+applyToggle\s*\([^)]*\)\s*\{", script_body
        )
        if not decl_match:
            continue
        open_brace, close_brace = _brace_span_from(
            script_body, decl_match.end() - 1
        )
        body = script_body[open_brace + 1 : close_brace]
        bg_match = _BACKGROUND_ASSIGNMENT_RE.search(body)
        save_match = re.search(r"\bsaveColor\s*\(", body)
        assert bg_match, "applyToggle does not assign the background"
        assert save_match, "applyToggle does not call saveColor"
        assert bg_match.start() < save_match.start(), (
            "the background assignment must precede the saveColor call "
            "inside applyToggle's source order"
        )
        return
    raise AssertionError("no applyToggle function found in any <script> block")
