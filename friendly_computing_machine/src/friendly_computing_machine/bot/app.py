import logging
from dataclasses import dataclass
from datetime import datetime, timedelta
from threading import Lock

from slack_bolt import App

from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    get_bot_slack_user_slack_ids,
    get_music_polls,
    get_slack_channel,
)
from friendly_computing_machine.src.friendly_computing_machine.models.music_poll import (
    MusicPoll,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackChannel,
)

__GLOBALS = {}


logger = logging.getLogger(__name__)
bot_config_lock = Lock()


def init_web_client(slack_bot_token: str):
    if "slack_web_client" in __GLOBALS:
        raise RuntimeError("double slack web client init")
    __GLOBALS["slack_web_client"] = SlackWebClientFCM(
        token=slack_bot_token,
        logger=logging.getLogger("slack_web"),
    )


# Slack app instance (initialized lazily)
_app_instance = None


def get_slack_app() -> App:
    """Get the Slack app instance, initializing it if needed."""
    global _app_instance
    if _app_instance is None:
        _app_instance = App(
            client=get_slack_web_client(), logger=logging.getLogger("slack_bolt")
        )
    return _app_instance


class _AppProxy:
    """Proxy object that defers to the real app when methods are called."""

    def __getattr__(self, name):
        return getattr(get_slack_app(), name)

    def __call__(self, *args, **kwargs):
        return get_slack_app()(*args, **kwargs)


# Create a proxy that can be imported and used with decorators
app = _AppProxy()


@dataclass
class MusicPollInfo:
    music_poll: MusicPoll
    slack_channel: SlackChannel


@dataclass
class SlackBotConfig:
    music_poll_infos: list[MusicPollInfo]
    BOT_SLACK_USER_IDS: set[str]
    as_of: datetime

    REFRESH_PERIOD: timedelta = timedelta(minutes=1)

    @classmethod
    def create(cls):
        # this is not an ideal solution, but it works for now
        music_polls = get_music_polls()
        music_poll_infos = [
            MusicPollInfo(
                music_poll=poll,
                slack_channel=get_slack_channel(poll.slack_channel_id),
            )
            for poll in music_polls
        ]

        return SlackBotConfig(
            music_poll_infos=music_poll_infos,
            BOT_SLACK_USER_IDS=get_bot_slack_user_slack_ids(),
            as_of=datetime.now(),
        )


def get_bot_config(should_ignore_cache: bool = False) -> SlackBotConfig:
    # assuming that this function is not thread safe when called by bolt and adding a lock
    # TODO - improve config access
    if should_ignore_cache:
        logger.info("ignoring slackbot config cache")
        return SlackBotConfig.create()
    if not bot_config_lock.acquire(timeout=10):
        raise RuntimeError("bot lock timeout")
    config = __GLOBALS.get("bot_config")
    if config is None:
        logger.info("creating slackbot config for the first time")
        config = SlackBotConfig.create()
    elif config.as_of + SlackBotConfig.REFRESH_PERIOD < datetime.now():
        logger.info("slackbot config has expired, refreshing")
        config = SlackBotConfig.create()
    __GLOBALS["bot_config"] = config
    bot_config_lock.release()
    return config


def get_slack_web_client() -> SlackWebClientFCM:
    client = __GLOBALS.get("slack_web_client")
    if client is None:
        raise RuntimeError("client not init")
    return client
