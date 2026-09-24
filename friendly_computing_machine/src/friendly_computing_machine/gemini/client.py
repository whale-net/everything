"""Module-level singleton for the `google-genai` SDK client.

Mirrors whagent/client.py's init_whagent_client()/get_whagent_client()
pattern: init once from a CLI callback, then every caller (gemini/ai.py,
temporal/ai/activity.py) fetches the same client instead of each holding
its own.
"""

from __future__ import annotations

import os
from typing import Optional

from google import genai

# Default Gemini model for every bare (no-model-name) call site this SDK
# replaced. Overridable via GEMINI_MODEL for testing or a model bump
# without a code change.
DEFAULT_GEMINI_MODEL = os.environ.get("GEMINI_MODEL", "gemini-2.5-flash")

_client: Optional[genai.Client] = None


def init_gemini_client(api_key: str) -> None:
    global _client
    if _client is not None:
        raise RuntimeError("double gemini client init")
    _client = genai.Client(api_key=api_key)


def get_gemini_client() -> genai.Client:
    if _client is None:
        raise RuntimeError("gemini client not initialized")
    return _client
