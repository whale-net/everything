import typer

app = typer.Typer(
    context_settings={"obj": {}},
)

shitposter_app = typer.Typer(help="Shitposter operator reads, paged for the shell.")
app.add_typer(shitposter_app, name="shitposter")

PageOption = typer.Option(1, "--page", min=1, help="1-based page number.")
PageSizeOption = typer.Option(20, "--page-size", min=1, max=100, help="Rows per page.")


@shitposter_app.command("history")
def shitposter_history(page: int = PageOption, page_size: int = PageSizeOption):
    """Attribute and lore changes with causes, newest first."""
    from friendly_computing_machine.src.friendly_computing_machine.bot.handlers.shitposter_operator import (
        page_history,
    )

    typer.echo(page_history(page, page_size))


@shitposter_app.command("runs")
def shitposter_runs(page: int = PageOption, page_size: int = PageSizeOption):
    """Reflector runs with rejection counts and reasons, newest first."""
    from friendly_computing_machine.src.friendly_computing_machine.bot.handlers.shitposter_operator import (
        page_runs,
    )

    typer.echo(page_runs(page, page_size))


# NO LONGER NEEDED
# BUT SOMETIMES IT IS, SO LEAVING IT FOR FUTURE USE
@app.command()
def update_helm_chart_version(ref: str, commit_count: int):
    # crude but it'll do

    if ref.startswith("refs/tags/v"):
        version = ref[len("refs/tags/v") :]
    else:
        version = f"v0.0.{commit_count}"

    file_lines: list[str] = []
    with open("./charts/friendly-computing-machine/Chart.yaml", "r") as chart:
        while True:
            line = chart.readline()
            if not line:
                break
            elif line.startswith("version:"):
                file_lines.append(f"version: {version}\n")
            else:
                file_lines.append(line)

    with open("./charts/friendly-computing-machine/Chart.yaml", "w") as chart:
        chart.writelines(file_lines)
