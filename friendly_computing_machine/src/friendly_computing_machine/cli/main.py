import logging

import typer
from temporalio import workflow

# TODO - this is working, but should it? is this just the same as doing all?
# anyway not doing anything super important so I'm sure it's fine.
# I'm not sure this is the right way to do this but whatever going to do it until it breaks
with workflow.unsafe.imports_passed_through():
    from friendly_computing_machine.cli.bot_cli import app as bot_app
    from friendly_computing_machine.cli.migration_cli import migration_app
    from friendly_computing_machine.cli.subscribe_cli import app as subscribe_app
    from friendly_computing_machine.cli.tools_cli import app as tool_app
    from friendly_computing_machine.cli.workflow_cli import app as workflow_app

logger = logging.getLogger(__name__)

app = typer.Typer()
app.add_typer(bot_app, name="bot")
app.add_typer(migration_app, name="migration")
app.add_typer(subscribe_app, name="subscribe")
app.add_typer(tool_app, name="tools")
app.add_typer(workflow_app, name="workflow")
