package history

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ProjectRef is a project an import can map a transcript folder to.
type ProjectRef struct {
	ID   int64
	Name string
	Path string
}

// encodeFolder is how Claude Code names a session folder: the project path
// with every character other than a letter or digit replaced by '-'
// (/home/pc/Documents/app becomes -home-pc-Documents-app).
var nonName = regexp.MustCompile(`[^A-Za-z0-9]`)

func encodeFolder(path string) string { return nonName.ReplaceAllString(path, "-") }

// Proposal maps a transcript folder to the project it probably belongs to.
type Proposal struct {
	Folder  string
	Project ProjectRef
	Exact   bool // the folder is the project's path, encoded (the strong match)
}

// Propose matches each transcript folder to a project: exactly when the
// folder is the encoded project path, otherwise by the project's folder name
// at the end of the folder (a PC path /home/pc/Documents/app gives "...-app").
// A name that fits two projects is not proposed; the owner maps it.
func Propose(folders []string, projects []ProjectRef) []Proposal {
	var out []Proposal
	for _, f := range folders {
		var exact, suffix []ProjectRef
		for _, p := range projects {
			switch {
			case encodeFolder(p.Path) == f:
				exact = append(exact, p)
			case strings.HasSuffix(f, "-"+encodeFolder(filepath.Base(p.Path))):
				suffix = append(suffix, p)
			}
		}
		switch {
		case len(exact) == 1:
			out = append(out, Proposal{Folder: f, Project: exact[0], Exact: true})
		case len(exact) == 0 && len(suffix) == 1:
			out = append(out, Proposal{Folder: f, Project: suffix[0]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out
}

// Folders lists the session folders under root (one per project, each holding
// *.jsonl transcripts), by name.
func Folders(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(root, e.Name(), "*.jsonl"))
		if len(files) > 0 {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Report is what an import did.
type Report struct {
	Files    int // transcripts read
	Turns    int // kept turns
	Chunks   int // chunks produced (scrubbed first)
	Scrubbed int // chunks that held a secret, now marked [redacted]
	New      int // chunks stored (the rest were already there)
	Embedded int // chunks that needed a vector
}

const embedBatch = 64

// ImportFolder reads every transcript in root/folder into project p of the
// workspace. Each turn is scrubbed before it is chunked, and each chunk
// before it is embedded or stored. Chunks already stored are skipped, so
// importing the same files again adds nothing and embeds nothing.
func ImportFolder(ctx context.Context, s *Store, emb Embedder, ws string, p ProjectRef, root, folder string) (Report, error) {
	var rep Report
	files, err := filepath.Glob(filepath.Join(root, folder, "*.jsonl"))
	if err != nil {
		return rep, err
	}
	sort.Strings(files)
	var pending []Chunk
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		n, err := s.add(ctx, emb, ws, p.ID, pending, &rep)
		rep.New += n
		pending = pending[:0]
		return err
	}
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return rep, err
		}
		turns, err := ParseTranscript(f)
		f.Close()
		if err != nil {
			return rep, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		rep.Files++
		rep.Turns += len(turns)
		for _, t := range turns {
			clean, n := Scrub(t.Text)
			t.Text = clean
			chunks, err := fit(ctx, emb, ChunkTurn(t))
			if err != nil {
				return rep, err
			}
			for _, c := range chunks {
				rep.Chunks++
				c.Scrubbed = n > 0
				if c.Scrubbed {
					rep.Scrubbed++
				}
				pending = append(pending, c)
				if len(pending) >= embedBatch {
					if err := flush(); err != nil {
						return rep, err
					}
				}
			}
		}
	}
	return rep, flush()
}

// fit splits any chunk the model cannot take whole (over maxPassageTokens)
// into pieces of the same turn, and numbers the parts of each turn from 0. An
// embedder that cannot count tokens (the tests' fake) keeps the chunks as they are.
func fit(ctx context.Context, emb Embedder, chunks []Chunk) ([]Chunk, error) {
	cnt, ok := emb.(Counter)
	if !ok {
		return chunks, nil
	}
	var out []Chunk
	part := 0
	for i, c := range chunks {
		if i == 0 || chunks[i-1].Session != c.Session || chunks[i-1].Turn != c.Turn {
			part = 0
		}
		pieces, err := splitToFit(ctx, cnt, c.Text)
		if err != nil {
			return nil, err
		}
		for _, p := range pieces {
			cc := c
			cc.Text = p
			cc.Part = part
			part++
			out = append(out, cc)
		}
	}
	return out, nil
}

// splitToFit cuts text in halves, at a space or newline near the middle, until
// every piece is at most maxPassageTokens.
func splitToFit(ctx context.Context, cnt Counter, text string) ([]string, error) {
	n, err := cnt.Count(ctx, text)
	if err != nil {
		return nil, err
	}
	rs := []rune(text)
	if n <= maxPassageTokens || len(rs) < 2 {
		return []string{text}, nil
	}
	mid := len(rs) / 2
	cut := mid
	for d := 0; d < 40; d++ {
		if i := mid - d; i > 0 && (rs[i] == ' ' || rs[i] == '\n') {
			cut = i
			break
		}
		if i := mid + d; i < len(rs) && (rs[i] == ' ' || rs[i] == '\n') {
			cut = i
			break
		}
	}
	var out []string
	for _, half := range []string{strings.TrimSpace(string(rs[:cut])), strings.TrimSpace(string(rs[cut:]))} {
		if half == "" {
			continue
		}
		pieces, err := splitToFit(ctx, cnt, half)
		if err != nil {
			return nil, err
		}
		out = append(out, pieces...)
	}
	return out, nil
}

// add stores the chunks that are not in the database yet, embedding only those.
func (s *Store) add(ctx context.Context, emb Embedder, ws string, project int64, chunks []Chunk, rep *Report) (int, error) {
	keys := make([]string, len(chunks))
	for i, c := range chunks {
		keys[i] = dedupeKey(ws, project, c)
	}
	have, err := s.existing(ctx, keys)
	if err != nil {
		return 0, err
	}
	var todo []Stored
	var texts []string
	for i, c := range chunks {
		if have[keys[i]] {
			continue
		}
		todo = append(todo, Stored{Chunk: c, Scrubbed: c.Scrubbed})
		texts = append(texts, c.Text)
	}
	if len(todo) == 0 {
		return 0, nil
	}
	vecs, err := emb.Embed(ctx, texts)
	if err != nil {
		return 0, err
	}
	if len(vecs) != len(todo) {
		return 0, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vecs), len(todo))
	}
	rep.Embedded += len(todo)
	for i := range todo {
		todo[i].Vec = vecs[i]
	}
	return s.Add(ctx, ws, project, todo)
}
