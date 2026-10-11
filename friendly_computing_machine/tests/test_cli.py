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


def test_brain_trigger_idea_generate_starts_a_run(monkeypatch, capsys):
    """brain-trigger <persona> idea_generate hands the kind to the brain runner's operator trigger."""
    from types import SimpleNamespace

    from friendly_computing_machine.src.friendly_computing_machine.cli import (
        workflow_cli,
    )
    from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain.base import (
        BrainJobResult,
    )

    calls = []

    async def fake_client():
        return "client"

    async def fake_trigger(client, task_queue, persona_id, job_kind, app_env):
        calls.append((client, task_queue, persona_id, job_kind, app_env))
        return BrainJobResult(run_id=11, status="succeeded")

    monkeypatch.setattr(workflow_cli, "get_temporal_client_async", fake_client)
    monkeypatch.setattr(workflow_cli, "trigger_brain_job_async", fake_trigger)
    monkeypatch.setattr(workflow_cli, "get_temporal_queue_name", lambda name: f"q-{name}")

    workflow_cli.cli_brain_trigger(SimpleNamespace(obj={"app_env": "dev"}), 7, "idea_generate")

    assert calls == [("client", "q-main", 7, "idea_generate", "dev")]
    assert "run_id=11 status=succeeded" in capsys.readouterr().out
