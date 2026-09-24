import asyncio
from typing import Any, Optional, Sequence, Union

from temporalio.client import Client
from temporalio.contrib.opentelemetry import TracingInterceptor
from temporalio.contrib.pydantic import pydantic_data_converter


class __GlobalConfig:
    temporal_host: Optional[str] = None
    queue_prefix: Optional[str] = None
    app_env: Optional[str] = None


def init_temporal(host: str, app_env: str):
    if __GlobalConfig.temporal_host is not None:
        raise RuntimeError("temporal host already set")
    __GlobalConfig.temporal_host = host
    __GlobalConfig.queue_prefix = f"fcm-{app_env}-"
    __GlobalConfig.app_env = app_env


def get_temporal_queue_name(name: str) -> str:
    if __GlobalConfig.queue_prefix is None:
        raise RuntimeError("temporal queue prefix not set")
    return f"{__GlobalConfig.queue_prefix}{name}"


def get_temporal_host() -> str:
    if __GlobalConfig.temporal_host is None:
        raise RuntimeError("temporal host not set")
    return __GlobalConfig.temporal_host


def get_app_env() -> str:
    """Return the app_env init_temporal was called with.

    Shared by any workflow-id scheme (e.g. the whagent thread relay's
    "fcm-{app_env}-whagent-thread-{channel}-{thread_ts}") that needs to
    derive the same id in more than one place without a DB round trip.
    """
    if __GlobalConfig.app_env is None:
        raise RuntimeError("app_env not set")
    return __GlobalConfig.app_env


async def get_temporal_client_async():
    host = get_temporal_host()
    return await Client.connect(
        host,
        data_converter=pydantic_data_converter,
        interceptors=[TracingInterceptor()],
    )


def execute_workflow(
    runner,
    workflow_args: Optional[Union[Sequence[Any], Any]] = None,
    **kwargs,
):
    return asyncio.run(execute_workflow_async(runner, workflow_args, **kwargs))


async def execute_workflow_async(
    runner,
    args: Optional[Union[Sequence[Any], Any]] = None,
    **kwargs,
):
    client = await get_temporal_client_async()

    # Handle different argument scenarios
    if args is None:
        # No arguments case
        result = await client.execute_workflow(
            runner,
            **kwargs,
        )
    elif isinstance(args, (list, tuple)):
        # Already a sequence - pass directly
        result = await client.execute_workflow(
            runner,
            args=args,
            **kwargs,
        )
    else:
        # Single argument that's not a sequence - wrap it
        result = await client.execute_workflow(
            runner,
            args=[args],
            **kwargs,
        )

    return result


def start_workflow(
    runner,
    workflow_args: Optional[Union[Sequence[Any], Any]] = None,
    **kwargs,
):
    """Start a workflow without waiting for it to finish.

    Unlike execute_workflow, this returns as soon as Temporal has accepted
    the start -- for long-lived workflows (e.g. the whagent thread relay)
    that a Slack handler must not block on.
    """
    return asyncio.run(start_workflow_async(runner, workflow_args, **kwargs))


async def start_workflow_async(
    runner,
    args: Optional[Union[Sequence[Any], Any]] = None,
    **kwargs,
):
    client = await get_temporal_client_async()

    if args is None:
        handle = await client.start_workflow(runner, **kwargs)
    elif isinstance(args, (list, tuple)):
        handle = await client.start_workflow(runner, args=args, **kwargs)
    else:
        handle = await client.start_workflow(runner, args=[args], **kwargs)

    return handle


def signal_workflow(workflow_id: str, signal, *signal_args):
    """Signal a running workflow by id without waiting on its result."""
    return asyncio.run(signal_workflow_async(workflow_id, signal, *signal_args))


async def signal_workflow_async(workflow_id: str, signal, *signal_args):
    client = await get_temporal_client_async()
    handle = client.get_workflow_handle(workflow_id)
    await handle.signal(signal, *signal_args)
