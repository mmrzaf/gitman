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
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	seen := make(map[int64]bool, len(migrations))
	for i, m := range migrations {
		if seen[m.version] {
			t.Fatalf("duplicate migration version %d", m.version)
		}
		seen[m.version] = true
		if i > 0 && migrations[i-1].version >= m.version {
			t.Fatalf("migrations not ordered at index %d", i)
		}
		if len(m.checksum) != 64 {
			t.Fatalf("missing SHA-256 checksum for %s", m.name)
		}
	}
}

func TestAppliedMigrationCannotBeRenamedOrModified(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	m := migrations[0]
	if err := validateAppliedMigration(m, m.name, m.checksum); err != nil {
		t.Fatalf("valid migration rejected: %v", err)
	}
	for _, c := range []struct{ name, checksum string }{
		{m.name + ".changed", m.checksum},
		{m.name, "tampered"},
	} {
		if err := validateAppliedMigration(m, c.name, c.checksum); err == nil {
			t.Fatalf("changed migration %q/%q accepted", c.name, c.checksum)
		}
	}
}
