import datetime
import logging

from opentelemetry import trace
from slack_bolt import Ack, Say

from friendly_computing_machine.src.friendly_computing_machine.bot.app import app
from friendly_computing_machine.src.friendly_computing_machine.bot.modal_builder import build_server_select_modal
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_client import SlackWebClientFCM
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    insert_genai_text,
    insert_slack_command,
    update_genai_text_response,
)
from friendly_computing_machine.src.friendly_computing_machine.models.genai import GenAITextCreate
from friendly_computing_machine.src.friendly_computing_machine.models.slack import SlackCommandCreate
from friendly_computing_machine.src.friendly_computing_machine.temporal.slack.workflow import (
    SlackContextGeminiWorkflow,
    SlackContextGeminiWorkflowParams,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.util import (
    execute_workflow,
    get_temporal_queue_name,
)

logger = logging.getLogger(__name__)
tracer = trace.get_tracer(__name__)


# Name TBD, good enough for now, and doesn't reserve "ai" as a slash command
@app.command("/wai")
def handle_whale_ai_command(ack: Ack, say: Say, command):
    with tracer.start_as_current_span("handle_whale_ai_command") as span:
        try:
            ack()
            logger.info("slack-ack")

            user_name = command["user_name"]
            user_id = command["user_id"]
            channel_id = command["channel_id"]
            text = command["text"]

            span.set_attribute("slack.command", "/wai")
            span.set_attribute("slack.user.id", user_id)
            span.set_attribute("slack.user.name", user_name)
            span.set_attribute("slack.channel.id", channel_id)
            span.set_attribute("slack.command.text", text)

            # TODO - move this entirely to the workflow
            # it is dangerous to do this here, because we are not in a transaction
            # and we don't want to pollute the database with a command that is not processed.
            # Addiionally, temporal will not be able to retry this if it fails.
            # use child workflow if we don't want to pollute existing workflow (probably a good idea, although not needed now)
            # be aggressive on concurrency
            command_create = SlackCommandCreate(
                caller_slack_user_id=user_id,
                command_base="/wai",
                command_text=text,
                slack_channel_slack_id=channel_id,
                created_at=datetime.datetime.now(),
            )
            db_command = insert_slack_command(command_create)
            span.set_attribute("db.command.inserted", True)
            span.set_attribute("db.command.id", db_command.id)

            # create and log request right away
            genai_text = insert_genai_text(
                GenAITextCreate(
                    slack_channel_slack_id=channel_id,
                    slack_user_slack_id=user_id,
                    prompt=text,
                    # TODO: am I stupid?
                    created_at=datetime.datetime.now(),
                )
            )
            span.set_attribute("db.genai_text.id", genai_text.id)
            # remove old method and replace with temporal
            # ai_response, ai_feedback, ai_safety = generate_text(username, text)
            # ai_response, _ = generate_text_with_slack_context(
            #     user_name, text, genai_text.slack_channel_slack_id
            # )
            workflow_id = f"test_id-command-wai-{channel_id}-{datetime.datetime.now()}"
            span.set_attribute("temporal.workflow.id", workflow_id)
            ai_response = execute_workflow(
                SlackContextGeminiWorkflow.run,
                SlackContextGeminiWorkflowParams(channel_id, text),
                id=workflow_id,
                # TODO - proper task queue differentation at some point
                task_queue=get_temporal_queue_name("main"),
            )
            span.set_attribute("temporal.workflow.executed", True)

            if ai_response is None:
                logger.info(
                    "%s (%s) just triggered a bad response. See genai_text=%s",
                    user_name,
                    user_id,
                    genai_text.id,
                )
                say(
                    "error processing your response. It has been entirely ignored and you should feel bad for trying."
                )
                update_genai_text_response(
                    genai_text_id=genai_text.id,
                    response="WARNING: bad response produced",
                )
                span.set_attribute("ai.response.status", "bad_response")
            else:
                update_genai_text_response(
                    genai_text_id=genai_text.id, response=ai_response
                )
                span.set_attribute("db.genai_text.response_updated", True)
                say(text=ai_response)
                span.set_attribute("slack.response.sent", True)
                span.set_attribute("ai.response.status", "success")

            logger.info("successfully processed /wai")
            span.set_status(trace.Status(trace.StatusCode.OK))
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            logger.exception("Error handling /wai command")
            # Reraise exception to be caught by global handler
            raise


@app.command("/test")
def handle_test_command(ack: Ack, say: Say, command, client: SlackWebClientFCM):
    with tracer.start_as_current_span("handle_test_command") as span:
        try:
            ack()
            logger.info("slack-ack")
            span.set_attribute("slack.command", "/test")
            span.set_attribute("slack.command.text", command["text"])
            # modal = ServerSelectModal(options=DUMMY_SERVERS)
            modal = build_server_select_modal()
            client.views_open(
                trigger_id=command["trigger_id"],
                view=modal.build(),
            )
        except Exception as e:
            span.record_exception(e)
            span.set_status(trace.Status(trace.StatusCode.ERROR, str(e)))
            logger.exception("Error handling /test command")
            # Reraise exception to be caught by global handler
            raise
        finally:
            # This block will always run
            logger.info("Finished handling /test command")
            span.end()
