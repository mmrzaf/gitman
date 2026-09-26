package postgres

import "testing"

func TestParseMigrationVersion(t *testing.T) {
	cases := []struct {
		name    string
		want    int64
		wantErr bool
	}{
		{"0001_init.sql", 1, false},
		{"0042_add_widgets.sql", 42, false},
		{"init.sql", 0, true},
		{"abc_init.sql", 0, true},
	}
	for _, c := range cases {
		got, err := parseMigrationVersion(c.name)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseMigrationVersion(%q): expected an error", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMigrationVersion(%q): unexpected error: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseMigrationVersion(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLoadMigrationsAreOrderedAndUnique(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("expected at least one migration")
	}
	seen := make(map[int64]bool, len(migrations))
	for i, m := range migrations {
		if seen[m.version] {
			t.Fatalf("duplicate migration version %d", m.version)
		}
		seen[m.version] = true
		if i > 0 && migrations[i-1].version >= m.version {
			t.Fatalf("migrations are not strictly ordered at index %d", i)
		}
	}
}
