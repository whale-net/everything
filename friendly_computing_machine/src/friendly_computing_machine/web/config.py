"""Configuration for fcm's OIDC link web endpoint, sourced from environment."""

import dataclasses
import os


@dataclasses.dataclass(frozen=True)
class WebConfig:
    """Everything the link flow needs, read once from the environment.

    The redirect URI is derived from the public URL so a deployment only has to
    get the Keycloak client's registered redirect right in one place.
    """

    public_url: str
    oidc_issuer_url: str
    oidc_client_id: str
    oidc_client_secret: str
    session_secret: str

    @property
    def callback_url(self) -> str:
        return f"{self.public_url.rstrip('/')}/link/callback"

    @classmethod
    def from_env(cls) -> "WebConfig":
        return cls(
            public_url=os.environ.get("FCM_WEB_PUBLIC_URL", ""),
            oidc_issuer_url=os.environ.get("FCM_OIDC_ISSUER_URL", ""),
            oidc_client_id=os.environ.get("FCM_OIDC_CLIENT_ID", ""),
            oidc_client_secret=os.environ.get("FCM_OIDC_CLIENT_SECRET", ""),
            session_secret=os.environ.get("FCM_WEB_SESSION_SECRET", ""),
        )
