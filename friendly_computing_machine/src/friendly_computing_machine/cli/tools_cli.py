import typer

app = typer.Typer(
    context_settings={"obj": {}},
)


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
