// Command render is the runnable entrypoint for krill/render (issue #2495,
// FR13-FR15): it renders one Product's current spec into the committed doc
// set (`PRODUCT.md` + `product/*.md`) and writes those four files under
// --out. FR15's one-way constraint applies here too -- this binary reads
// krill and writes the filesystem; it never writes krill.
//
// Usage:
//
//	bazel run //krill/render/cmd:render -- --product krill --out krill/
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/render"
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
	product := flag.String("product", "", "name of the Product to render (required)")
	out := flag.String("out", "", "directory to write PRODUCT.md + product/*.md into (required)")
	databaseURL := flag.String("database-url", os.Getenv("PG_DATABASE_URL"), "Postgres connection string (defaults to PG_DATABASE_URL, then //libs/go/db's own fallback)")
	flag.Parse()

	if *product == "" {
		return fmt.Errorf("--product is required")
	}
	if *out == "" {
		return fmt.Errorf("--out is required")
	}

	ctx := context.Background()
	pool, err := db.NewPool(ctx, *databaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	st := store.New(pool)

	// M1 seeds exactly one `scope` row (krill/ARCHITECTURE.md's "The
	// scope table (LB1)") and exposes no CRUD over it; a later multi-scope
	// capability (C22) is what would make this a real selector instead of
	// "the only row there is."
	var scopeID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM scope LIMIT 1`).Scan(&scopeID); err != nil {
		return fmt.Errorf("resolve scope: %w", err)
	}

	products, err := st.Products().ListCurrentByScope(ctx, scopeID)
	if err != nil {
		return fmt.Errorf("list products: %w", err)
	}
	var productID uuid.UUID
	found := false
	for _, p := range products {
		if strings.EqualFold(p.Name, *product) {
			productID = p.ID
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("no product named %q in scope %s", *product, scopeID)
	}

	files, err := render.Render(ctx, render.NewStoreSource(st), scopeID, productID)
	if err != nil {
		return fmt.Errorf("render %q: %w", *product, err)
	}

	for rel, content := range files.FileMap() {
		path := filepath.Join(*out, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Printf("wrote %s\n", path)
	}

	return nil
}
