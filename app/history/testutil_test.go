package history

import (
	"context"
	"hash/fnv"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devark28/xhaos/app/sqlitedb"
)

// fakeEmbedder hashes words into a fixed number of buckets: texts that share
// words are close. It stands in for the model in tests.
type fakeEmbedder struct {
	dim   int
	calls int // texts embedded so far
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.calls += len(texts)
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.dim)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			h.Write([]byte(strings.Trim(w, ".,:;!?()[]{}\"'`")))
			v[int(h.Sum32())%f.dim]++
		}
		out[i] = normalize(v)
	}
	return out, nil
}

func openStore(t *testing.T) *Store {
	t.Helper()
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	return NewStore(db)
}
