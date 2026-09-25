def main() -> None:
    # Imported lazily so that importing a submodule (e.g. the FastAPI web app
    # for OpenAPI codegen) does not drag in the whole CLI and its heavy deps.
    from friendly_computing_machine.src.friendly_computing_machine.cli.main import (
        app,
    )

    app()
