"""Fold of retired/merged attributes and stale lore into consolidated lore.

Reflector folds run inside the reflect apply transaction; stale-lore folds run
inside the snapshot job. Operator-retired rows never fold.

Red-proof: dropping the retired_by_operator filter in fold_reflector_retirements
turns the operator-retire test red; changing the <= cutoff in fold_stale_lore to
< turns the boundary test red.
"""

import datetime
import random

import pytest
from sqlalchemy import event
from sqlalchemy.pool import StaticPool
from sqlmodel import Session, create_engine, select

from friendly_computing_machine.src.friendly_computing_machine.db import util as db_util
from friendly_computing_machine.src.friendly_computing_machine.db.dal import (
    shitposter_memory_dal as memory,
)
from friendly_computing_machine.src.friendly_computing_machine.models.base import Base
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter import (
    ShitposterBrainJobKind,
    ShitposterBrainJobRun,
    ShitposterPersona,
    ShitposterReflectorRun,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_context import (
    ShitposterContextSnapshot,
    ShitposterSnapshotItem,
)
from friendly_computing_machine.src.friendly_computing_machine.models.shitposter_memory import (
    ShitposterLoreEntry,
    ShitposterLoreKindEnum,
    ShitposterMemoryCauseKindEnum,
    ShitposterMemoryChange,
    ShitposterMemoryEntityKindEnum,
    ShitposterMemoryOperationEnum,
    ShitposterPersonaAttribute,
)
from friendly_computing_machine.src.friendly_computing_machine.temporal.shitposter_brain import (
    fold,
    reflect,
    snapshot,
)

T0 = datetime.datetime(2026, 10, 1, 12, 0, tzinfo=datetime.UTC)
NOW = datetime.datetime.now(datetime.UTC)
DAY = datetime.timedelta(days=1)
SECOND = datetime.timedelta(seconds=1)
TABLES = [
    ShitposterPersona.__table__,
    ShitposterBrainJobRun.__table__,
    ShitposterReflectorRun.__table__,
    ShitposterContextSnapshot.__table__,
    ShitposterSnapshotItem.__table__,
    ShitposterPersonaAttribute.__table__,
    ShitposterLoreEntry.__table__,
    ShitposterMemoryChange.__table__,
]


@pytest.fixture
def engine(monkeypatch):
    engine = create_engine(
        "sqlite://", connect_args={"check_same_thread": False}, poolclass=StaticPool
    )

    @event.listens_for(engine, "connect")
    def _attach(dbapi_conn, _):
        dbapi_conn.execute("ATTACH DATABASE ':memory:' AS fcm")

    Base.metadata.create_all(engine, tables=TABLES)
    monkeypatch.setitem(db_util.__GLOBALS, "engine", engine)
    monkeypatch.setattr(snapshot, "RNG_FACTORY", lambda: random.Random(42))
    return engine


def _persona(s: Session) -> int:
    persona = ShitposterPersona(name="shitposter")
    s.add(persona)
    s.commit()
    return persona.id


def _run(s: Session, persona_id: int, job_kind: str, status: str = "running") -> int:
    run = ShitposterBrainJobRun(
        persona_id=persona_id,
        job_kind=job_kind,
        trigger="operator",
        status=status,
        started_at=T0,
        finished_at=None if status == "running" else T0,
    )
    s.add(run)
    s.commit()
    return run.id


def _attr(s: Session, persona_id: int, key: str, text: str) -> int:
    row = memory.add_attribute(persona_id, key, text, "operator", cause_ref="seed", session=s)
    s.commit()
    return row.id


def _lore(s: Session, persona_id: int, text: str, created: datetime.datetime) -> int:
    row = memory.add_lore(
        persona_id,
        text,
        ShitposterLoreKindEnum.HIT.value,
        ShitposterMemoryCauseKindEnum.POST_ENGAGEMENT.value,
        cause_ref="test",
        now=created,
        session=s,
    )
    s.commit()
    return row.id


def _appear(s: Session, persona_id: int, lore_id: int, at: datetime.datetime, version: int) -> None:
    run_id = _run(s, persona_id, ShitposterBrainJobKind.SNAPSHOT.value, status="succeeded")
    snap = ShitposterContextSnapshot(
        persona_id=persona_id,
        version=version,
        created_at=at,
        brain_job_run_id=run_id,
        token_count=0,
        rendered_text="",
    )
    s.add(snap)
    s.flush()
    s.add(
        ShitposterSnapshotItem(
            snapshot_id=snap.id,
            item_kind=ShitposterMemoryEntityKindEnum.LORE.value,
            item_id=lore_id,
            rank=1,
            is_random_pick=False,
        )
    )
    s.commit()


def _reflect_payload(persona_id: int, ops: list[dict]) -> dict:
    return {
        "no_input": False,
        "persona_id": persona_id,
        "input_count": 0,
        "carried_over_count": 0,
        "inputs": [],
        "ops": ops,
        "rejections": [],
    }


def _fold_changes(s: Session, entity_kind: str, entity_id: int) -> list[ShitposterMemoryChange]:
    return list(
        s.exec(
            select(ShitposterMemoryChange).where(
                ShitposterMemoryChange.entity_kind == entity_kind,
                ShitposterMemoryChange.entity_id == entity_id,
                ShitposterMemoryChange.operation == ShitposterMemoryOperationEnum.FOLD.value,
            )
        ).all()
    )


def _is_current_lore(s: Session, lore_id: int) -> bool:
    return s.get(ShitposterLoreEntry, lore_id).valid_to is None


# --- reflector folds -------------------------------------------------------


def test_reflector_retire_folds_into_consolidated_lore(engine):
    with Session(engine) as s:
        pid = _persona(s)
        keep = _attr(s, pid, "humor", "dry humor")
        gone = _attr(s, pid, "pace", "short sentences")
        run_id = _run(s, pid, "reflect")

    ops = [{"op": "retire", "key": "pace", "attribute_id": gone, "causes": ["suggestion:1"]}]
    with Session(engine) as s:
        reflect._apply(s, run_id, _reflect_payload(pid, ops))
        s.commit()

    with Session(engine) as s:
        consolidated = memory.current_consolidated_lore(pid, session=s)
        assert consolidated is not None
        assert consolidated.kind == ShitposterLoreKindEnum.CONSOLIDATED.value
        assert "short sentences" in consolidated.text
        assert s.get(ShitposterPersonaAttribute, gone).valid_to is not None
        assert s.get(ShitposterPersonaAttribute, keep).valid_to is None
        changes = _fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, gone)
        assert len(changes) == 1
        assert changes[0].cause_kind == ShitposterMemoryCauseKindEnum.FOLD.value
        assert changes[0].reflector_run_id == str(run_id)


def test_reflector_merge_folds_the_merged_away_attribute(engine):
    with Session(engine) as s:
        pid = _persona(s)
        into = _attr(s, pid, "humor", "dry humor")
        from_id = _attr(s, pid, "wit", "quick wit")
        run_id = _run(s, pid, "reflect")

    ops = [{"op": "merge", "from_id": from_id, "into_id": into, "causes": ["suggestion:1"]}]
    with Session(engine) as s:
        reflect._apply(s, run_id, _reflect_payload(pid, ops))
        s.commit()

    with Session(engine) as s:
        consolidated = memory.current_consolidated_lore(pid, session=s)
        assert consolidated is not None and "quick wit" in consolidated.text
        assert s.get(ShitposterPersonaAttribute, into).valid_to is None
        assert len(_fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, from_id)) == 1


def _operator_retire_attribute(s: Session, persona_id: int) -> tuple[int, int]:
    """Returns (closed predecessor id, operator-retired successor id)."""
    attr_id = _attr(s, persona_id, "pace", "short sentences")
    memory.operator_retire(
        ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, attr_id, "operator-sub", session=s
    )
    successor = s.exec(
        select(ShitposterPersonaAttribute).where(
            ShitposterPersonaAttribute.persona_id == persona_id,
            ShitposterPersonaAttribute.attribute_key == "pace",
            ShitposterPersonaAttribute.valid_to.is_(None),  # type: ignore[union-attr]
        )
    ).one()
    assert successor.retired_by_operator is True
    return attr_id, successor.id


def test_reflector_cannot_retire_operator_retired_attribute_so_nothing_folds(engine):
    with Session(engine) as s:
        pid = _persona(s)
        attr_id, _ = _operator_retire_attribute(s, pid)
        run_id = _run(s, pid, "reflect")

    ops = [{"op": "retire", "key": "pace", "attribute_id": attr_id, "causes": ["suggestion:1"]}]
    with Session(engine) as s:
        with pytest.raises(LookupError):
            reflect._apply(s, run_id, _reflect_payload(pid, ops))
        s.rollback()

    with Session(engine) as s:
        assert memory.current_consolidated_lore(pid, session=s) is None
        assert _fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, attr_id) == []


def test_operator_retired_successor_is_not_folded(engine):
    with Session(engine) as s:
        pid = _persona(s)
        _, successor_id = _operator_retire_attribute(s, pid)
        run_id = _run(s, pid, "reflect")

        fold.fold_reflector_retirements(s, pid, [successor_id], run_id)
        s.commit()

        assert memory.current_consolidated_lore(pid, session=s) is None
        assert _fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, successor_id) == []


def test_operator_retired_predecessor_id_is_not_folded(engine):
    # the closed predecessor carries retired_by_operator=False; the flag lives on its successor
    with Session(engine) as s:
        pid = _persona(s)
        attr_id, _ = _operator_retire_attribute(s, pid)
        run_id = _run(s, pid, "reflect")

        fold.fold_reflector_retirements(s, pid, [attr_id], run_id)
        s.commit()

        assert memory.current_consolidated_lore(pid, session=s) is None
        assert _fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, attr_id) == []


# --- stale lore folds ------------------------------------------------------


def test_stale_lore_folds_at_30_day_boundary(engine):
    with Session(engine) as s:
        pid = _persona(s)
        at_boundary = _lore(s, pid, "boundary joke", T0 - 90 * DAY)
        _appear(s, pid, at_boundary, T0 - 30 * DAY, version=1)
        just_inside = _lore(s, pid, "inside joke", T0 - 90 * DAY)
        _appear(s, pid, just_inside, T0 - 30 * DAY + SECOND, version=2)
        just_past = _lore(s, pid, "past joke", T0 - 90 * DAY)
        _appear(s, pid, just_past, T0 - 30 * DAY - SECOND, version=3)

    with Session(engine) as s:
        folded = fold.fold_stale_lore(pid, now=T0, session=s)
        s.commit()
        assert folded == 2

    with Session(engine) as s:
        assert not _is_current_lore(s, at_boundary)
        assert _is_current_lore(s, just_inside)
        assert not _is_current_lore(s, just_past)
        consolidated = memory.current_consolidated_lore(pid, session=s)
        assert "boundary joke" in consolidated.text and "past joke" in consolidated.text
        assert len(_fold_changes(s, ShitposterMemoryEntityKindEnum.LORE.value, at_boundary)) == 1


def test_never_appeared_lore_uses_creation_time(engine):
    with Session(engine) as s:
        pid = _persona(s)
        old_unseen = _lore(s, pid, "old unseen", T0 - 30 * DAY)
        young_unseen = _lore(s, pid, "young unseen", T0 - 29 * DAY)

    with Session(engine) as s:
        assert fold.fold_stale_lore(pid, now=T0, session=s) == 1
        s.commit()

    with Session(engine) as s:
        assert not _is_current_lore(s, old_unseen)
        assert _is_current_lore(s, young_unseen)


def test_recent_appearance_keeps_old_lore_current(engine):
    with Session(engine) as s:
        pid = _persona(s)
        lore_id = _lore(s, pid, "long-running bit", T0 - 365 * DAY)
        _appear(s, pid, lore_id, T0 - 1 * DAY, version=1)

    with Session(engine) as s:
        assert fold.fold_stale_lore(pid, now=T0, session=s) == 0
        s.commit()

    with Session(engine) as s:
        assert _is_current_lore(s, lore_id)


def test_operator_retired_lore_never_folds(engine):
    with Session(engine) as s:
        pid = _persona(s)
        lore_id = _lore(s, pid, "operator pulled", T0 - 90 * DAY)
        memory.operator_retire(
            ShitposterMemoryEntityKindEnum.LORE.value, lore_id, "operator-sub", session=s
        )
        assert s.get(ShitposterLoreEntry, lore_id).retired_by_operator is True

    with Session(engine) as s:
        assert fold.fold_stale_lore(pid, now=T0, session=s) == 0
        s.commit()

    with Session(engine) as s:
        assert memory.current_consolidated_lore(pid, session=s) is None


def test_stale_fold_without_session_commits_on_its_own(engine):
    with Session(engine) as s:
        pid = _persona(s)
        lore_id = _lore(s, pid, "solo fold", T0 - 90 * DAY)

    assert fold.fold_stale_lore(pid, now=T0) == 1

    with Session(engine) as s:
        assert not _is_current_lore(s, lore_id)
        assert memory.current_consolidated_lore(pid, session=s) is not None


# --- atomicity -------------------------------------------------------------


def test_reflector_fold_rolls_back_with_apply(engine, monkeypatch):
    with Session(engine) as s:
        pid = _persona(s)
        attr_id = _attr(s, pid, "pace", "short sentences")
        run_id = _run(s, pid, "reflect")

    def boom(session, persona_id, retired_ids, run):
        raise RuntimeError("failure after the fold hook")

    monkeypatch.setattr(reflect, "_FOLD_HOOKS", [*reflect._FOLD_HOOKS, boom])
    ops = [{"op": "retire", "key": "pace", "attribute_id": attr_id, "causes": ["suggestion:1"]}]
    with Session(engine) as s:
        with pytest.raises(RuntimeError):
            reflect._apply(s, run_id, _reflect_payload(pid, ops))
        s.rollback()

    with Session(engine) as s:
        assert memory.current_consolidated_lore(pid, session=s) is None
        assert s.get(ShitposterPersonaAttribute, attr_id).valid_to is None
        assert _fold_changes(s, ShitposterMemoryEntityKindEnum.ATTRIBUTE.value, attr_id) == []


def test_stale_fold_rolls_back_with_snapshot_job(engine, monkeypatch):
    with Session(engine) as s:
        pid = _persona(s)
        lore_id = _lore(s, pid, "old joke", NOW - 60 * DAY)
        run_id = _run(s, pid, ShitposterBrainJobKind.SNAPSHOT.value)

    def boom(*args, **kwargs):
        raise RuntimeError("failure after the fold")

    monkeypatch.setattr(snapshot, "select_items", boom)
    with Session(engine) as s:
        with pytest.raises(RuntimeError):
            snapshot._apply(s, run_id, {"persona_id": pid})
        s.rollback()

    with Session(engine) as s:
        assert _is_current_lore(s, lore_id)
        assert memory.current_consolidated_lore(pid, session=s) is None


def test_snapshot_job_folds_stale_lore_before_building(engine):
    with Session(engine) as s:
        pid = _persona(s)
        stale_id = _lore(s, pid, "stale joke", NOW - 60 * DAY)
        run_id = _run(s, pid, ShitposterBrainJobKind.SNAPSHOT.value)

    with Session(engine) as s:
        outcome = snapshot._apply(s, run_id, {"persona_id": pid})
        s.commit()
        assert outcome.status == "succeeded"

    with Session(engine) as s:
        assert not _is_current_lore(s, stale_id)
        assert "stale joke" in memory.current_consolidated_lore(pid, session=s).text


# --- consolidated text cap -------------------------------------------------


def test_consolidated_text_capped_at_500_chars_keeping_newest():
    existing = "old joke " * 100
    text = fold._consolidated_text(existing, ["fresh one", "fresh two"])
    assert len(text) <= fold.MAX_CONSOLIDATED_CHARS
    assert text.endswith("fresh two")
    assert not text.startswith(" ")
