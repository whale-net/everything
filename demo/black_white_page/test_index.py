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


def _read_source() -> str:
    return _INDEX_HTML.read_text(encoding="utf-8")


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
    source = _read_source()
    for script_match in _SCRIPT_BLOCK_RE.finditer(source):
        script_body = script_match.group(1)
        assert not _BACKGROUND_ASSIGNMENT_RE.search(script_body), (
            "a <script> block assigns the background -- the initial "
            "background must come from markup/CSS, not JavaScript"
        )
