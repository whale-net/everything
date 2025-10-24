import datetime
import json
import logging
import time
from dataclasses import dataclass
from typing import Optional

from amqpstorm import Channel, Connection

from generated.py.manman.status_api.api.default_api import DefaultApi as ManManStatusAPI
from generated.py.manman.status_api.models.external_status_info import ExternalStatusInfo
from generated.py.manman.status_api.models.status_type import StatusType
from friendly_computing_machine.src.friendly_computing_machine.bot.app import (
    SlackWebClientFCM,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.slack_models import (
    create_manman_status_blocks,
)
from friendly_computing_machine.src.friendly_computing_machine.bot.util import (
    slack_send_message,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    # get_manman_status_update_from_create,  # DEAD CODE - unused import
    get_slack_message_from_id,  # TODO - be less lazy with this
    get_slack_special_channel_type_from_name,
    get_slack_special_channels_from_type,
    # insert_manman_status_update,  # DEAD CODE - unused import, using upsert instead
    update_manman_status_update,
)
from friendly_computing_machine.src.friendly_computing_machine.db.dal.manman_dal import (
    upsert_manman_status_update,
)
from friendly_computing_machine.src.friendly_computing_machine.models.manman import (
    ManManStatusUpdate,
    ManManStatusUpdateCreate,
)
from friendly_computing_machine.src.friendly_computing_machine.models.slack import (
    SlackMessage,
)
from friendly_computing_machine.src.friendly_computing_machine.util import (
    datetime_to_ts,
)

logger = logging.getLogger(__name__)


@dataclass
class QueueConfig:
    """Configuration for a message queue."""

    name: str
    routing_keys: list[str]


class ManManSubscribeService:
    """
    ManMan Subscribe Service - handles events from RabbitMQ topics and sends
    notifications to Slack based on worker and instance status changes.

    This service subscribes to manman topics for:
    - Worker lifecycle events (start, running, lost, complete)
    - Instance lifecycle events (start, init, running, lost, complete)

    Events are processed to:
    - Send formatted Slack messages with action buttons

    SIMPLIFIED SYNCHRONOUS CONTROL FLOW:
    1. Initialization (__init__): Sets up RabbitMQ connection, channel, and queue configs.
    2. Service Start (start()): Declares durable queues, binds them to exchange, registers consumers.
    3. Message Callback (_amqp_message_callback): Directly processes messages synchronously.
    4. Service Stop (stop()): Stops consuming and closes channel.
    """

    def __init__(
        self,
        app_env: str,
        rabbitmq_connection: Connection,
        slack_api: SlackWebClientFCM,
        manman_status_api: ManManStatusAPI,
    ):
        """
        Initialize the ManMan Subscribe Service.

        Args:
            rabbitmq_connection: Connection object for RabbitMQ
            slack_api: SlackWebClientFCM for Slack interactions
            manman_status_api: ManManStatusAPI for status interactions
            app_env: Application environment string
        """
        self._rabbitmq_connection = rabbitmq_connection
        self._channel: Channel = rabbitmq_connection.channel()
        self._slack_api = slack_api
        self._is_running = False
        self._manman_status_api = manman_status_api
        self._app_env = app_env

        # Queue configuration using proper classes
        self._exchange = "external_service_events"
        self._queue = QueueConfig(
            name=f"fcm-{self._app_env}.manman.generic.status",
            routing_keys=[
                "worker.*.status",
                "game_server_instance.*.status",
            ],
        )

        # Hardcoding for now
        self._manman_channel_type = get_slack_special_channel_type_from_name(
            "manman_dev"
        )
        if self._manman_channel_type:
            self._manman_channel_tups = get_slack_special_channels_from_type(
                self._manman_channel_type
            )
        else:
            logger.warning(
                "No ManMan channel type found, using default channel for notifications."
            )
            self._manman_channel_tups = []

        logger.info("ManMan Subscribe Service initialized")

    def start(self):
        """
        Start the subscribe service in synchronous mode.
        Declares queues, sets up consumers, and starts blocking message consumption.
        """
        logger.info("Starting ManMan Subscribe Service (Synchronous Mode)")
        if self._is_running:
            logger.warning("Service is already running.")
            return

        self._is_running = True

        try:
            # Set up queues and bindings
            self._channel.queue.declare(queue=self._queue.name, durable=True)
            for routing_key in self._queue.routing_keys:
                self._channel.queue.bind(
                    exchange=self._exchange,
                    queue=self._queue.name,
                    routing_key=routing_key,
                )
                logger.info(f"Binding created for routing key: {routing_key}")

            # Register consumer for this queue
            self._channel.basic.consume(
                callback=self._amqp_message_callback,
                queue=self._queue.name,
                no_ack=False,  # Manual acknowledgment
            )
            logger.info(f"Consumer set up for queue: {self._queue.name}")

            logger.info("Starting synchronous message consumption...")
            # This blocks until stop_consuming() is called
            self._channel.start_consuming()
            logger.info("ManMan Subscribe Service stopped consuming.")

        except Exception as e:
            logger.error(f"Failed to start ManMan Subscribe Service: {e}")
            self._is_running = False
            raise

    def stop(self):
        """
        Stop the subscribe service gracefully.
        Signals the consumer to stop and closes the channel.
        """
        logger.info("Stopping ManMan Subscribe Service")
        if not self._is_running:
            logger.info("Service not running.")
            return

        self._is_running = False

        if self._channel and self._channel.is_open:
            logger.info("Stopping AMQP consumer...")
            try:
                self._channel.stop_consuming()
                logger.info("AMQP consumer stopped.")
            except Exception as e:
                logger.error(f"Error stopping AMQP consumer: {e}")

            logger.info("Closing AMQP channel.")
            try:
                self._channel.close()
                logger.info("AMQP channel closed.")
            except Exception as e:
                logger.error(f"Error closing AMQP channel: {e}")

        self._channel = None
        logger.info("ManMan Subscribe Service stopped.")

    def _amqp_message_callback(self, message):
        """
        Synchronous callback executed when a message is received.
        """
        try:
            logger.info(
                f"Received message on queue {self._queue.name} (delivery_tag: {message.delivery_tag})"
            )

            # Parse message
            message_data = json.loads(message.body)
            logger.debug(f"Message JSON parsed: {message_data}")

            status_info = ExternalStatusInfo.from_dict(message_data)
            logger.debug(f"ExternalStatusInfo deserialized: {status_info}")

            if status_info.as_of < datetime.datetime.now(
                datetime.timezone.utc
            ) - datetime.timedelta(minutes=5):
                self._channel.basic.ack(delivery_tag=message.delivery_tag)
                logger.warning(
                    f"tag {message.delivery_tag} status update {status_info.worker_id} is too old: {status_info.as_of}. Ignoring update."
                )
                return

            # Process message based on type
            if status_info.worker_id:
                self._handle_status_update(status_info)
            elif status_info.game_server_instance_id:
                self._handle_status_update(status_info)
            else:
                logger.warning(f"Unknown status info type: {status_info}")

            # Acknowledge message
            self._channel.basic.ack(delivery_tag=message.delivery_tag)
            logger.info(f"Message {message.delivery_tag} acknowledged.")

        except json.JSONDecodeError as e:
            logger.warning(
                f"Invalid JSON in message (delivery_tag: {message.delivery_tag}): {e}. Body: {message.body[:200]}"
            )
            self._channel.basic.reject(delivery_tag=message.delivery_tag, requeue=False)
            logger.info(f"Message {message.delivery_tag} (invalid JSON) rejected.")
        except Exception as e:
            logger.error(
                f"Error processing message (delivery_tag: {message.delivery_tag}): {e}",
                exc_info=True,
            )
            self._channel.basic.reject(delivery_tag=message.delivery_tag, requeue=False)
            logger.warning(
                f"Message {message.delivery_tag} rejected due to processing error."
            )

    def _handle_worker_status_update(self, status_info: ExternalStatusInfo):
        """
        Handle worker status update events.

        Args:
            status_info: ExternalStatusInfo object containing worker status information
        """
        logger.info(
            f"Worker {status_info.worker_id} status update: {status_info.status_info_id}"
        )

        # TODO: temporal workflow
        # create a workflow for each worker/server instance by-id
        # when a worker/server instance is created, start a workflow
        # when a worker/server instance is updated, send a signal to the workflow
        # the workflow should handle the status updates and slack logic
        # this will be the synchronization point for updates hopefully.
        # can also keep track of event timestamp and make sure that we don't ever go from a current state to an older state
        # however, for now I am just going to write to the database and send slack messages
        # the database will ultimately be the source of truth regardless of implementation
        # however, the control flow around it will be different
        # TODO - write to database
        # TODO - if new -> send slack message
        # TODO - if exists -> update slack message
        # TODO - always retrieve info in paralell (? what is this requirement mean? just temporal note?)
        # from friendly_computing_machine.bot.util import slack_send_message

        status_update_create = ManManStatusUpdateCreate.from_status_info(status_info)

        # Use upsert pattern - handles both create and update cases gracefully
        # This removes the need for time.sleep(3) race condition workaround
        status_update: ManManStatusUpdate = upsert_manman_status_update(
            status_update_create
        )

        # Handle Slack notification based on whether we need to create or update
        if status_info.status_type == StatusType.CREATED:
            # Create new Slack message
            message = self._handle_slack_notification(status_info)
            status_update.slack_message_id = message.id
            status_update = update_manman_status_update(status_update)
            logger.info(
                "Created Slack notification for worker %s: message_id=%s",
                status_update.service_id,
                message.id,
            )
        elif status_info.status_type in [
            StatusType.RUNNING,
            StatusType.COMPLETE,
            StatusType.LOST,
        ]:
            # Update existing Slack message
            slack_message = get_slack_message_from_id(status_update.slack_message_id)
            if not slack_message:
                # Wait for message to be created in Slack
                # This is a retry hack until temporal workflow is implemented
                # shouldn't be as needed with synchronous workflow
                time.sleep(2)
                slack_message = get_slack_message_from_id(
                    status_update.slack_message_id
                )
                if not slack_message:
                    logger.error(
                        "Slack message not found for status update %s after waiting",
                        status_update.service_id,
                    )
                    return
            if slack_message is None:
                # avoid double sending messages. need create, for now...
                logger.error(
                    "Slack message not found for status update %s",
                    status_update.service_id,
                )
                return
            self._handle_slack_notification(
                status_info,
                update_ts=datetime_to_ts(slack_message.ts),
            )
            logger.info(
                "Updated Slack notification for worker %s",
                status_update.service_id,
            )
        else:
            # Includes INITIALIZING, which is not handled here, but is for server
            logger.warning(
                f"Unhandled worker status type for worker {status_info.worker_id}: {status_info.status_type}"
            )

        logger.info(
            f"Worker {status_info.worker_id} status update processed: {status_info.status_info_id}"
        )

    def _handle_status_update(self, status_info: ExternalStatusInfo):
        """
        Handle worker status update events.

        Args:
            status_info: ExternalStatusInfo object containing worker status information
        """
        logger.info("handling %s", status_info)

        # TODO: temporal workflow
        # create a workflow for each worker/server instance by-id
        # when a worker/server instance is created, start a workflow
        # when a worker/server instance is updated, send a signal to the workflow
        # the workflow should handle the status updates and slack logic
        # this will be the synchronization point for updates hopefully.
        # can also keep track of event timestamp and make sure that we don't ever go from a current state to an older state
        # however, for now I am just going to write to the database and send slack messages
        # the database will ultimately be the source of truth regardless of implementation
        # however, the control flow around it will be different
        # TODO - write to database
        # TODO - if new -> send slack message
        # TODO - if exists -> update slack message
        # TODO - always retrieve info in paralell (? what is this requirement mean? just temporal note?)
        # from friendly_computing_machine.bot.util import slack_send_message

        status_update_create = ManManStatusUpdateCreate.from_status_info(status_info)

        # Use upsert pattern - handles both create and update cases gracefully
        # This removes the need for time.sleep(3) race condition workaround
        status_update: ManManStatusUpdate = upsert_manman_status_update(
            status_update_create
        )

        # Handle Slack notification based on whether we need to create or update
        if status_info.status_type == StatusType.CREATED:
            # Create new Slack message
            message = self._handle_slack_notification(status_info)
            status_update.slack_message_id = message.id
            status_update = update_manman_status_update(status_update)
            logger.info(
                "Created Slack notification for type %s with id %s: message_id=%s",
                status_update.service_type,
                status_update.service_id,
                message.id,
            )
        elif status_info.status_type in [
            StatusType.RUNNING,
            StatusType.COMPLETE,
            StatusType.LOST,
        ]:
            # Update existing Slack message
            slack_message = get_slack_message_from_id(status_update.slack_message_id)
            if not slack_message:
                # Wait for message to be created in Slack
                # This is a retry hack until temporal workflow is implemented
                # shouldn't be as needed with synchronous workflow
                time.sleep(2)
                slack_message = get_slack_message_from_id(
                    status_update.slack_message_id
                )
                if not slack_message:
                    logger.error(
                        "Slack message not found for status update %s after waiting",
                        status_update.service_id,
                    )
                    return
            if slack_message is None:
                # avoid double sending messages. need create, for now...
                logger.error(
                    "Slack message not found for status update %s",
                    status_update.service_id,
                )
                return
            self._handle_slack_notification(
                status_info,
                update_ts=datetime_to_ts(slack_message.ts),
            )
            logger.info(
                "Updated Slack notification for worker %s",
                status_update.service_id,
            )
        else:
            logger.warning("Unhandled status type %s", status_info.status_type)

        logger.info("status_info %s handled successfully", status_info)

    def _handle_slack_notification(
        self,
        status_info: ExternalStatusInfo,
        update_ts: Optional[str] = None,
    ) -> SlackMessage:
        for (
            special_channel,
            slack_channel,
            special_channel_type,
        ) in self._manman_channel_tups:
            try:
                message_block = create_manman_status_blocks(
                    special_channel_type=special_channel_type,
                    current_status=status_info,
                )
                message = slack_send_message(
                    channel=slack_channel.slack_id,
                    blocks=message_block,
                    update_ts=update_ts,
                )
                logger.info(f"Sent Slack notification to channel {special_channel}")
                return message
            except Exception as e:
                logger.error(
                    f"Failed to send Slack notification to channel {special_channel}: {e}"
                )
                raise
