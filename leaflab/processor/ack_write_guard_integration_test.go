//go:build integration

// This file only builds under the "integration" build tag (see
// repository_hw_history_integration_test.go's own doc comment for why).
//
// It proves FR45's database-level half: migration 032_ack_write_guard's
// BEFORE UPDATE trigger on device_config actually refuses a write to
// accepted/acked_at/rejection_reason that does not carry the
// leaflab.ack_write transaction-local marker -- "in any role" is
// satisfied because the guard is keyed on that marker, not on which
// database role issued the UPDATE (see the migration's own doc comment
// for why a column-privilege GRANT/REVOKE cannot make that distinction
// here: the API and processor share one DB role). This file exercises
// that guarantee directly against real SQL, standing in for "any API
// caller, or an elevated admin running an ad-hoc UPDATE" with a bare
// UPDATE statement carrying no marker at all -- indistinguishable, as far
// as the trigger is concerned, from any other write path that hasn't set
// it. The application-layer half -- no leaflab/api repository method
// exists that could even attempt such a write -- is
// leaflab/api/repository_ack_surface_test.go.
//
// Schema is self-contained DDL: board/device_config mirror their real
// shape (migrations 007/031) and the trigger itself is migration 032's up
// migration, copied verbatim rather than depended on, per dbtest's own
// doc comment on Options.Schema staying hermetic.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/processor:ack_write_guard_integration_test --test_output=all
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// ackWriteGuardSchema mirrors board/device_config's real shape and
// migration 032_ack_write_guard's trigger, copied verbatim from
// leaflab/migrate/migrations/032_ack_write_guard.up.sql.
const ackWriteGuardSchema = `
	CREATE TABLE board (
		board_id  BIGSERIAL PRIMARY KEY,
		device_id VARCHAR(64) NOT NULL UNIQUE
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id),
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE FUNCTION enforce_device_config_ack_write_guard() RETURNS TRIGGER AS $$
	BEGIN
		IF (NEW.accepted IS DISTINCT FROM OLD.accepted
			OR NEW.acked_at IS DISTINCT FROM OLD.acked_at
			OR NEW.rejection_reason IS DISTINCT FROM OLD.rejection_reason)
		   AND coalesce(current_setting('leaflab.ack_write', true), '') <> 'on' THEN
			RAISE EXCEPTION 'device_config.accepted/acked_at/rejection_reason are writable only from the device ack path (FR45)';
		END IF;
		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_device_config_ack_write_guard
		BEFORE UPDATE ON device_config
		FOR EACH ROW
		EXECUTE FUNCTION enforce_device_config_ack_write_guard();
`

// seedBoardAndPendingConfig inserts one board with one pending (unacked)
// device_config row at the given version, and returns (boardID, configID).
func seedBoardAndPendingConfig(ctx context.Context, t *testing.T, db *dbtest.Postgres, deviceID string, version int64) (boardID, configID int64) {
	t.Helper()
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID,
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO device_config (board_id, version, config_json) VALUES ($1, $2, '{}'::jsonb) RETURNING config_id`,
		boardID, version,
	).Scan(&configID); err != nil {
		t.Fatalf("insert device_config: %v", err)
	}
	return boardID, configID
}

// TestAckWriteGuard_DirectUpdateWithoutMarker_Refused proves FR45's core
// claim: an UPDATE touching accepted/acked_at/rejection_reason issued with
// no leaflab.ack_write marker set is refused by the trigger -- regardless
// of which role or process issued it (the API and processor share one DB
// role, so this bare statement is indistinguishable from an elevated
// admin's ad-hoc UPDATE or any hypothetical future API repository method;
// see this file's doc comment).
func TestAckWriteGuard_DirectUpdateWithoutMarker_Refused(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: ackWriteGuardSchema})
	_, configID := seedBoardAndPendingConfig(ctx, t, db, "leaflab-ackguard-direct", 1)

	_, err := db.Pool.Exec(ctx,
		`UPDATE device_config SET accepted = TRUE, acked_at = NOW() WHERE config_id = $1`, configID)
	if err == nil {
		t.Fatal("direct UPDATE of accepted/acked_at with no ack_write marker succeeded, want the trigger to refuse it (FR45)")
	}
	if !strings.Contains(err.Error(), "FR45") {
		t.Errorf("UPDATE error = %v, want the trigger's FR45 exception message", err)
	}

	var accepted bool
	if scanErr := db.Pool.QueryRow(ctx, `SELECT accepted FROM device_config WHERE config_id = $1`, configID).Scan(&accepted); scanErr != nil {
		t.Fatalf("read back accepted: %v", scanErr)
	}
	if accepted {
		t.Error("device_config.accepted is TRUE after a refused UPDATE -- the trigger let a forged write through")
	}
}

// TestAckWriteGuard_RejectionReasonAlone_AlsoRefused proves the guard
// covers each of the three columns individually -- a write touching only
// rejection_reason (accepted/acked_at left as-is) is refused the same way,
// not just a write that touches all three together.
func TestAckWriteGuard_RejectionReasonAlone_AlsoRefused(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: ackWriteGuardSchema})
	_, configID := seedBoardAndPendingConfig(ctx, t, db, "leaflab-ackguard-reason-only", 1)

	_, err := db.Pool.Exec(ctx,
		`UPDATE device_config SET rejection_reason = 'forged' WHERE config_id = $1`, configID)
	if err == nil {
		t.Fatal("direct UPDATE of rejection_reason alone with no ack_write marker succeeded, want the trigger to refuse it")
	}
}

// TestAckWriteGuard_UnrelatedColumnUpdate_Unaffected proves the guard is
// scoped to the three ack columns only: a write that changes neither of
// them (config_json here) passes through untouched, per the migration's
// own doc comment ("an UPDATE that leaves all three unchanged ... passes
// through untouched").
func TestAckWriteGuard_UnrelatedColumnUpdate_Unaffected(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: ackWriteGuardSchema})
	_, configID := seedBoardAndPendingConfig(ctx, t, db, "leaflab-ackguard-unrelated", 1)

	if _, err := db.Pool.Exec(ctx,
		`UPDATE device_config SET config_json = '{"changed": true}'::jsonb WHERE config_id = $1`, configID); err != nil {
		t.Fatalf("UPDATE of an unrelated column was refused, want it to pass through untouched: %v", err)
	}
}

// TestAckWriteGuard_AckDeviceConfig_SetsMarkerAndSucceeds proves the one
// legitimate writer -- Repository.AckDeviceConfig, the real call site
// behind leaflab/processor/handler.go's handleConfigAck -- sets the
// leaflab.ack_write marker inside its own transaction and so is not
// refused by the same trigger the two tests above prove is otherwise
// strict.
func TestAckWriteGuard_AckDeviceConfig_SetsMarkerAndSucceeds(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: ackWriteGuardSchema})
	boardID, _ := seedBoardAndPendingConfig(ctx, t, db, "leaflab-ackguard-legit", 7)

	repo := NewRepository(db.Pool)
	pushedAt, ackedAt, err := repo.AckDeviceConfig(ctx, boardID, 7, true, "")
	if err != nil {
		t.Fatalf("AckDeviceConfig: %v", err)
	}
	if ackedAt.Before(pushedAt) {
		t.Errorf("ackedAt %v is before pushedAt %v", ackedAt, pushedAt)
	}

	var accepted bool
	if scanErr := db.Pool.QueryRow(ctx,
		`SELECT accepted FROM device_config WHERE board_id = $1 AND version = 7`, boardID,
	).Scan(&accepted); scanErr != nil {
		t.Fatalf("read back accepted: %v", scanErr)
	}
	if !accepted {
		t.Error("device_config.accepted is FALSE after AckDeviceConfig, want TRUE -- the legitimate write path was refused by its own guard")
	}
}

// TestAckWriteGuard_MarkerDoesNotSurviveIntoASubsequentTransaction proves
// SET LOCAL's scope is exactly one transaction: a bare UPDATE issued in a
// fresh connection/transaction after AckDeviceConfig's transaction has
// already committed (and reset the marker) is still refused -- the
// exemption does not leak forward onto a later, unrelated write against
// the same row, even one immediately following a legitimate ack.
func TestAckWriteGuard_MarkerDoesNotSurviveIntoASubsequentTransaction(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: ackWriteGuardSchema})
	boardID, configID := seedBoardAndPendingConfig(ctx, t, db, "leaflab-ackguard-noleak", 3)

	repo := NewRepository(db.Pool)
	if _, _, err := repo.AckDeviceConfig(ctx, boardID, 3, true, ""); err != nil {
		t.Fatalf("AckDeviceConfig: %v", err)
	}

	// A later, unrelated attempt to flip the outcome -- e.g. an ad-hoc
	// "fix" run against the same row -- must be refused exactly as it
	// would have been before the legitimate ack ever happened.
	_, err := db.Pool.Exec(ctx,
		`UPDATE device_config SET accepted = FALSE, rejection_reason = 'forged after the fact' WHERE config_id = $1`, configID)
	if err == nil {
		t.Fatal("UPDATE after AckDeviceConfig's transaction committed succeeded with no marker set, want the trigger to still refuse it")
	}
}
