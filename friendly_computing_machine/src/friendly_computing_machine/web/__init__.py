"""fcm's inbound HTTP surface: the Slack -> Keycloak identity-link endpoint.

Exposes a FastAPI ``app`` (and a ``create_app`` factory) that performs a
standard OIDC authorization-code login against the Keycloak realm whagent-net
uses and, on success, writes the Slack -> Keycloak mapping.
"""

from friendly_computing_machine.src.friendly_computing_machine.web.app import (
    app,
    create_app,
)

__all__ = ["app", "create_app"]
