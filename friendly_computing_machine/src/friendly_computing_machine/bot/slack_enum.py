from enum import StrEnum


class Emoji(StrEnum):
    """
    Enum for Slack emojis.
    """

    def __str__(self):
        return f":{self.value}:"

    SATELLITE_ANTENNA = "satellite_antenna"
    QUESTION = "question"
    SPINNING_GNOME = "rotate_gnome"
    # spin middle gnome
    MEGNO = "megno"
    GNOME_CHILD = "gnomechild"
    GNOME_SHAKE = "gnome_shake"
    GNOME_COOL = "gnomechildcool"
    CHICKEN_JOCKEY = "chicken_jockey"
    RAIN_GNOME = "rain_gnome"
    ZOOM_GNOME = "zoom_gnome"
