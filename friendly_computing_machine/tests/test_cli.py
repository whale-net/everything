import typer


def test_cli_main_imports():
    """Importing the CLI entry point must not raise.

    fcm_lib bundles every subcommand (bot/worker/web/...) into one
    binary, so this import chain pulls in both whagent's proto-generated
    client and the OTLP log exporter in the same process -- the combination
    that broke web_openapi_spec's generator with a "google" namespace
    collision (native protobuf's bundled runtime vs. the OTLP exporter's
    googleapis-common-protos dependency). This is the same import fcm_cli's
    own entry point (__init__.py's main()) performs at startup.
    """
    from friendly_computing_machine.src.friendly_computing_machine.cli.main import (
        app,
    )

    assert isinstance(app, typer.Typer)
