import functools

from slack_sdk import WebClient


class SlackWebClientFCM(WebClient):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)

    @functools.cached_property
    def team_id(self) -> str:
        """
        :return: returns cached current team_id
        """
        team_info_response = self.team_info()
        if team_info_response.status_code != 200:
            raise RuntimeError(
                f"Failed to retrieve team info, expected HTTP 200 but got {team_info_response.status_code}"
            )
        return team_info_response.get("team").get("id")
