package migrate

import (
	"embed"
	"io/fs"
	"testing"
)

//go:embed testdata/migrations/*.sql
var sourceTestMigrations embed.FS

//go:embed testdata/migrations_extra/*.sql
var sourceTestExtraMigrations embed.FS

func TestMergedFS_ReadDir_CombinesAllSources(t *testing.T) {
	merged, err := newMergedFS([]Source{
		{FS: sourceTestMigrations, Dir: "testdata/migrations"},
		{FS: sourceTestExtraMigrations, Dir: "testdata/migrations_extra"},
	})
	if err != nil {
		t.Fatalf("newMergedFS: %v", err)
	}

	des, err := fs.ReadDir(merged, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	want := map[string]bool{
		"0001_create_widgets.up.sql":      true,
		"0001_create_widgets.down.sql":    true,
		"0002_add_widgets_price.up.sql":   true,
		"0002_add_widgets_price.down.sql": true,
		"900001_extra_thing.up.sql":       true,
		"900001_extra_thing.down.sql":     true,
	}
	if len(des) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(des), len(want), des)
	}
	for _, d := range des {
		if !want[d.Name()] {
			t.Errorf("unexpected entry %q in merged dir listing", d.Name())
		}
	}
}

func TestMergedFS_Open_ReadsFromEitherSource(t *testing.T) {
	merged, err := newMergedFS([]Source{
		{FS: sourceTestMigrations, Dir: "testdata/migrations"},
		{FS: sourceTestExtraMigrations, Dir: "testdata/migrations_extra"},
	})
	if err != nil {
		t.Fatalf("newMergedFS: %v", err)
	}

	for _, name := range []string{"0001_create_widgets.up.sql", "900001_extra_thing.up.sql"} {
		f, err := merged.Open(name)
		if err != nil {
			t.Fatalf("Open(%q): %v", name, err)
		}
		f.Close()
	}

	if _, err := merged.Open("does_not_exist.sql"); err == nil {
		t.Fatal("Open(does_not_exist.sql): expected error, got nil")
	}
}
