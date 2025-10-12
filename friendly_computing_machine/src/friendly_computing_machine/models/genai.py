import datetime

from sqlalchemy import Column, DateTime, func
from sqlmodel import Field

from friendly_computing_machine.models.base import Base


class GenAITextBase(Base):
    slack_channel_slack_id: str = Field(index=True)
    slack_user_slack_id: str = Field(index=True)
    prompt: str
    created_at: datetime.datetime = Field(
        sa_column=Column(
            DateTime(timezone=True), server_default=func.current_timestamp(), index=True
        ),
    )
    response: str = Field(nullable=True, default=None)
    response_as_of: datetime.datetime = Field(index=True, nullable=True, default=None)


class GenAIText(GenAITextBase, table=True):
    id: int = Field(default=None, nullable=False, primary_key=True)
    slack_channel_id: int = Field(
        nullable=True, foreign_key="slackchannel.id", index=True
    )
    slack_user_id: int = Field(nullable=True, foreign_key="slackuser.id", index=True)


class GenAITextCreate(GenAITextBase):
    # TODO surely a better pattern exists
    def to_genai_text(self) -> GenAIText:
        return GenAIText(
            slack_channel_slack_id=self.slack_channel_slack_id,
            slack_user_slack_id=self.slack_user_slack_id,
            prompt=self.prompt,
            created_at=self.created_at,
            response=self.response,
            response_as_of=self.response_as_of,
        )
