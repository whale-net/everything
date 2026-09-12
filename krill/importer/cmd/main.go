// Command import is the runnable entrypoint for krill/importer (issue
// #2492, FR16, FR17): a Swarm Operator-triggered, one-time write that
// parses a `PRODUCT.md` + `product/*.md` doc set into krill's spec
// entities and prints the entity-id report. It is gated on `init` (FR3):
// the caller must pass a krill session id `POST /sessions/init` actually
// minted, exactly like every other write path in this milestone.
//
// --source-revision is required (FR12, NFR3, issue #2548): the caller
// supplies the repo commit SHA --path was imported from, recorded on the
// import_completion row for the audit trail. krill does not shell out to
// git to discover this itself. A second run against a --path already
// recorded as complete for the resolved session's scope refuses before
// parsing anything (importer.ErrAlreadyImported) -- FR12's one-time,
// one-way guarantee.
//
// Usage:
//
//	bazel run //krill/importer/cmd:import -- --path krill --session-id <uuid> --source-revision <sha>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/importer"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/libs/go/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("path", "", "root directory of the product's PRODUCT.md + product/*.md doc set (required)")
	sessionIDFlag := flag.String("session-id", "", "krill session id minted by POST /sessions/init (required, FR3)")
	sourceRevision := flag.String("source-revision", "", "repo commit SHA --path was imported from (required, FR12/NFR3: krill does not shell out to git to discover this)")
	databaseURL := flag.String("database-url", os.Getenv("PG_DATABASE_URL"), "Postgres connection string (defaults to PG_DATABASE_URL, then //libs/go/db's own fallback)")
	flag.Parse()

	if *path == "" {
		return fmt.Errorf("--path is required")
	}
	if *sessionIDFlag == "" {
		return fmt.Errorf("--session-id is required (FR3: import is gated on init)")
	}
	if *sourceRevision == "" {
		return fmt.Errorf("--source-revision is required (FR12/NFR3: the commit SHA --path was imported from)")
	}
	sessionID, err := uuid.Parse(*sessionIDFlag)
	if err != nil {
		return fmt.Errorf("--session-id: invalid UUID: %w", err)
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *databaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	st := store.New(pool)
	sessions := store.NewSessionStore(pool)

	report, err := importer.Import(ctx, st, sessions, sessionID, *path, *sourceRevision)
	if err != nil {
		return err
	}

	fmt.Print(report.Render())
	return nil
}
