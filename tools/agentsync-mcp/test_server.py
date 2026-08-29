import threading
import time

import pytest

from server import (
    POLL_INTERVAL_S,
    _end_session as end_session,
    _join_session as join_session,
    _leave_session as leave_session,
    _session_status as session_status,
    _start_session as start_session,
    _sync as sync,
)


@pytest.fixture(autouse=True)
def state_dir(tmp_path, monkeypatch):
    monkeypatch.setenv("AGENTSYNC_STATE_DIR", str(tmp_path))


def test_start_session_generates_id_and_does_not_join():
    result = start_session()
    assert result["session_id"]
    status = session_status(result["session_id"])
    assert status["participants"] == {}


def test_start_session_duplicate_errors():
    start_session("dup")
    with pytest.raises(ValueError):
        start_session("dup")


def test_join_and_status():
    start_session("s1")
    join_session("s1", "alice")
    join_session("s1", "bob")

    status = session_status("s1")
    assert set(status["participants"]) == {"alice", "bob"}
    assert status["ended_at"] is None

    slim = session_status("s1", slim=True)
    assert set(slim["participants"]) == {"alice", "bob"}


def test_join_nonexistent_session_errors():
    with pytest.raises(ValueError):
        join_session("nope", "alice")


def test_leave_removes_from_active_participants():
    start_session("s2")
    join_session("s2", "alice")
    join_session("s2", "bob")
    leave_session("s2", "bob")

    status = session_status("s2", slim=True)
    assert status["participants"] == ["alice"]


def test_sync_requires_prior_join():
    start_session("s3")
    with pytest.raises(ValueError):
        sync("s3", "ghost", "hi", timeout_s=1)


def test_sync_returns_immediately_when_alone():
    start_session("s4")
    join_session("s4", "alice")
    result = sync("s4", "alice", "hello?", timeout_s=5)
    assert result["status"] == "peer_left"


def test_sync_rendezvous_between_two_participants():
    start_session("s5")
    join_session("s5", "alice")
    join_session("s5", "bob")

    outcome: dict = {}

    def bob_thread():
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            status = session_status("s5")
            if status["pending_messages"].get("bob", 0) > 0:
                break
            time.sleep(0.05)
        outcome["bob"] = sync("s5", "bob", "hi alice", timeout_s=5)

    t = threading.Thread(target=bob_thread)
    t.start()
    alice_result = sync("s5", "alice", "hi bob", timeout_s=5)
    t.join(timeout=5)

    assert alice_result["status"] == "message"
    assert alice_result["from"] == "bob"
    assert alice_result["message"] == "hi alice"

    assert outcome["bob"]["status"] == "message"
    assert outcome["bob"]["from"] == "alice"
    assert outcome["bob"]["message"] == "hi bob"


def test_leave_session_wakes_blocked_sync():
    start_session("s6")
    join_session("s6", "alice")
    join_session("s6", "bob")

    outcome: dict = {}

    def alice_thread():
        outcome["alice"] = sync("s6", "alice", "ping", timeout_s=5)

    t = threading.Thread(target=alice_thread)
    t.start()
    time.sleep(POLL_INTERVAL_S * 2)
    leave_session("s6", "bob")
    t.join(timeout=5)

    assert outcome["alice"]["status"] == "peer_left"


def test_end_session_wakes_blocked_sync():
    start_session("s7")
    join_session("s7", "alice")
    join_session("s7", "bob")

    outcome: dict = {}

    def alice_thread():
        outcome["alice"] = sync("s7", "alice", "ping", timeout_s=5)

    t = threading.Thread(target=alice_thread)
    t.start()
    time.sleep(POLL_INTERVAL_S * 2)
    end_session("s7")
    t.join(timeout=5)

    assert outcome["alice"]["status"] == "session_ended"


def test_sync_times_out(monkeypatch):
    start_session("s8")
    join_session("s8", "alice")
    join_session("s8", "bob")  # bob never syncs, so alice can't get peer_left

    result = sync("s8", "alice", "ping", timeout_s=1)
    assert result["status"] == "timeout"
