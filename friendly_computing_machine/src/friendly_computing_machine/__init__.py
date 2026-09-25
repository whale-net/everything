def main() -> None:
    # Imported lazily so that importing another submodule of this package (e.g.
    # web.app, for OpenAPI generation) doesn't require the full CLI -- and
    # everything it pulls in -- to be on the dependency closure.
    from friendly_computing_machine.src.friendly_computing_machine.cli.main import (
        app,
    )

    app()
