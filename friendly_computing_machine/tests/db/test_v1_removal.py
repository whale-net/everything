"""The manman V1 removal actually holds.

Two things are easy to undo by accident and expensive to notice late: a
leftover import of the generated manman V1 clients, which puts V1 back on
FCM's compile-time dependency list, and a `manmanstatusupdate` model or
migration reappearing, which would resurrect a table the forward migration
dropped. The route tables this milestone retains are guarded separately by
`//friendly_computing_machine/tests:test_route_table`.
"""

import ast
import importlib.util
import os
from pathlib import Path

import pytest

# Module paths that existed only to serve manman V1.
V1_MODULES = (
    "friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.main",
    "friendly_computing_machine.src.friendly_computing_machine.bot.subscribe.service",
    "friendly_computing_machine.src.friendly_computing_machine.manman.api",
    "friendly_computing_machine.src.friendly_computing_machine.manman.util",
    "friendly_computing_machine.src.friendly_computing_machine.models.manman",
    "friendly_computing_machine.src.friendly_computing_machine.db.dal.manman_dal",
)

DROPPED_TABLE = "manmanstatusupdate"

_V1_CLIENT_IMPORT = "generated.py.manman"

_SRC_REL = "friendly_computing_machine/src/friendly_computing_machine"
_VERSIONS_REL = "friendly_computing_machine/src/migrations/versions"


def _repo_root() -> Path:
    """The workspace root, from runfiles under Bazel or from this file locally.

    Both layouts put `friendly_computing_machine/` at the same place relative
    to the root, so the two candidates below serve the Bazel and repo-root
    pytest runs alike.
    """
    runfiles = os.environ.get("TEST_SRCDIR")
    if runfiles:
        candidate = Path(runfiles, os.environ.get("TEST_WORKSPACE", ""))
        if (candidate / _SRC_REL).is_dir():
            return candidate
    for parent in Path(__file__).resolve().parents:
        if (parent / _SRC_REL).is_dir():
            return parent
    raise RuntimeError(f"could not locate {_SRC_REL} from {__file__}")


@pytest.mark.parametrize("module", V1_MODULES)
def test_v1_module_is_gone(module):
    try:
        spec = importlib.util.find_spec(module)
    except ModuleNotFoundError:
        # The whole parent package went with it, which is the stronger form of gone.
        spec = None
    assert spec is None, (
        f"{module} is back; manman V1 support was removed and the generated "
        "clients it imports must not return to FCM's build"
    )


def test_fcm_source_imports_no_generated_manman_clients():
    offenders = []
    for source in sorted(_repo_root().joinpath(_SRC_REL).rglob("*.py")):
        tree = ast.parse(source.read_text(), filename=str(source))
        for node in ast.walk(tree):
            names = []
            if isinstance(node, ast.Import):
                names = [alias.name for alias in node.names]
            elif isinstance(node, ast.ImportFrom) and node.module:
                names = [node.module]
            offenders += [
                f"{source.relative_to(_repo_root())}:{node.lineno}: {name}"
                for name in names
                if name.startswith(_V1_CLIENT_IMPORT)
            ]

    assert not offenders, (
        "FCM must not import the generated manman V1 clients:\n"
        + "\n".join(offenders)
    )


def _tables_op(migration: Path, func_name: str, op_name: str) -> set[str]:
    """Table-name literals `func_name` passes to `op.<op_name>()`."""
    tree = ast.parse(migration.read_text(), filename=str(migration))
    names = set()
    for func in ast.walk(tree):
        if not isinstance(func, ast.FunctionDef) or func.name != func_name:
            continue
        for call in ast.walk(func):
            if not isinstance(call, ast.Call):
                continue
            if not (isinstance(call.func, ast.Attribute) and call.func.attr == op_name):
                continue
            if call.args and isinstance(call.args[0], ast.Constant):
                if isinstance(call.args[0].value, str):
                    names.add(call.args[0].value)
    return names


def test_manmanstatusupdate_is_absent_from_the_migrated_schema():
    """The DROP is a forward migration, so the chain must end without the table.

    Read from the migration source rather than a built schema: the chain is
    what a deployed database actually gets. Unlike the route-table exerciser's
    walk, this one counts `drop_table` in upgrade() too -- a table retired by a
    forward migration is exactly the case here, and reading only downgrade()
    would miss it.
    """
    versions = _repo_root() / _VERSIONS_REL
    present: set[str] = set()
    for migration in sorted(versions.glob("*.py")):
        created = _tables_op(migration, "upgrade", "create_table")
        dropped = _tables_op(migration, "upgrade", "drop_table") | _tables_op(
            migration, "downgrade", "drop_table"
        )
        present = (present | created) - (dropped - created)

    assert DROPPED_TABLE not in present, (
        f"{DROPPED_TABLE} is created and never dropped by the migration chain; "
        "the manman V1 relay is gone and applied migrations are never edited, "
        "so the table has to stay dropped"
    )
