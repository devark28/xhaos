package prefs

import (
	"path/filepath"
	"testing"

	"github.com/devark28/xhaos/app/sqlitedb"
)

func TestAppearanceRoundTripAndLimits(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	Migrate(db)
	s := NewService(db)
	if v, err := s.Get("appearance"); err != nil || string(v) != "null" {
		t.Fatalf("empty: %s %v", v, err)
	}
	if err := s.Set("appearance", []byte(`{"theme":"light","scale":1.15}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Get("appearance"); string(v) != `{"theme":"light","scale":1.15}` {
		t.Fatalf("got %s", v)
	}
	for _, bad := range [][]byte{[]byte(`"light"`), []byte(`[1]`), []byte(`{`)} {
		if s.Set("appearance", bad) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if s.Set("anything", []byte(`{}`)) == nil {
		t.Error("accepted an unknown key")
	}
}
