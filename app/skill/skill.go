// Package skill holds skills: instructions (and files) an agent loads when
// a task calls for them, in the format Claude Code uses: a folder with a
// SKILL.md whose front matter gives the skill's name and description.
//
// Two sources:
//   - workspace skills, uploaded as one .skill file (a zip of that folder),
//     kept beside the database and read without unpacking;
//   - project skills, folders in a project's .claude/skills/ or
//     .xhaos/skills/, read where they are (they can be exported as .skill).
//
// Each can be switched off. An agent's prompt lists the enabled skills it can
// use (name and description only); skill_read loads one, or one of its files.
// A bash call that names skills gets their folders read-only for that command
// ($SKILL_<NAME>), so a skill's scripts can run; a workspace skill is unpacked
// for that once, into a folder of its own.
package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/devark28/xhaos/app/apperr"
	"github.com/devark28/xhaos/app/eventlog"
	"github.com/devark28/xhaos/app/project"
	"github.com/devark28/xhaos/app/sqlitedb"
)

const EventChanged = "skill.changed"

const (
	maxZip   = 20 << 20 // a .skill file
	maxTotal = 60 << 20 // its contents, unpacked
	maxFiles = 1000
	maxRead  = 100_000 // characters skill_read returns
)

// Folders inside a project that hold project skills, in order of precedence.
var ProjectDirs = []string{".xhaos/skills", ".claude/skills"}

const schema = `
CREATE TABLE IF NOT EXISTS skills (
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	name         TEXT NOT NULL,
	description  TEXT NOT NULL,
	enabled      INTEGER NOT NULL DEFAULT 1,
	prefix       TEXT NOT NULL DEFAULT '', -- the folder SKILL.md sits in, inside the zip
	size         INTEGER NOT NULL,
	files        INTEGER NOT NULL,
	sha          TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (workspace_id, name)
);
CREATE TABLE IF NOT EXISTS skill_toggles (
	workspace_id TEXT NOT NULL,
	project_id   INTEGER NOT NULL,
	name         TEXT NOT NULL,
	enabled      INTEGER NOT NULL,
	PRIMARY KEY (workspace_id, project_id, name)
);`

func Migrate(db *sql.DB) error { _, err := db.Exec(schema); return err }

// Skill is a skill as the page and agents see it.
type Skill struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Source      string    `json:"source"` // workspace | project
	ProjectID   int64     `json:"projectId,omitempty"`
	Folder      string    `json:"folder,omitempty"` // a project skill's folder, relative to the project
	Enabled     bool      `json:"enabled"`
	Files       int       `json:"files"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ProjectSkills are one project's skills.
type ProjectSkills struct {
	ProjectID int64   `json:"projectId"`
	Project   string  `json:"project"`
	Skills    []Skill `json:"skills"`
}

type Listing struct {
	Workspace []Skill         `json:"workspace"`
	Projects  []ProjectSkills `json:"projects"`
}

type Service struct {
	db      *sql.DB
	dir     string // .skill files: dir/<workspace>/<name>.skill; unpacked copies: dir/unpacked/<sha>
	publish eventlog.Publisher
	now     func() time.Time
}

func NewService(db *sql.DB, dir string, publish eventlog.Publisher) (*Service, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if publish == nil {
		publish = func(eventlog.Event) {}
	}
	return &Service{db: db, dir: dir, publish: publish, now: time.Now}, nil
}

// ---- reading skills ------------------------------------------------------------

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// frontMatter reads name and description from SKILL.md's front matter
// (the YAML between the first two "---" lines; plain, quoted, and folded
// or literal block values).
func frontMatter(text string) (name, description string, err error) {
	text = strings.TrimPrefix(text, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("SKILL.md must start with front matter (---, name:, description:, ---)")
	}
	fields := map[string]string{}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if value == ">" || value == "|" || value == ">-" || value == "|-" {
			var block []string
			for i+1 < len(lines) && (strings.HasPrefix(lines[i+1], " ") || strings.HasPrefix(lines[i+1], "\t") || strings.TrimSpace(lines[i+1]) == "") && strings.TrimSpace(lines[i+1]) != "---" {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			value = strings.TrimSpace(strings.Join(block, " "))
		}
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		fields[key] = value
	}
	name, description = fields["name"], fields["description"]
	if !nameRE.MatchString(name) {
		return "", "", fmt.Errorf("SKILL.md's name %q must be lowercase letters, digits and hyphens", name)
	}
	if description == "" {
		return "", "", errors.New("SKILL.md needs a description: it is how agents know when to use the skill")
	}
	if len(description) > 1024 {
		description = description[:1024]
	}
	return name, description, nil
}

// bundle is a skill's files: a zip (workspace) or a folder (project).
type bundle interface {
	list() []string // relative paths, SKILL.md included
	read(name string) ([]byte, error)
}

type zipBundle struct {
	r      *zip.Reader
	prefix string
}

func (z zipBundle) list() []string {
	var out []string
	for _, f := range z.r.File {
		if !f.FileInfo().IsDir() && strings.HasPrefix(f.Name, z.prefix) {
			out = append(out, strings.TrimPrefix(f.Name, z.prefix))
		}
	}
	sort.Strings(out)
	return out
}

func (z zipBundle) read(name string) ([]byte, error) {
	f, err := z.r.Open(z.prefix + name)
	if err != nil {
		return nil, fmt.Errorf("the skill has no file %s", name)
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxTotal))
}

type dirBundle string

func (d dirBundle) list() []string {
	var out []string
	_ = filepath.WalkDir(string(d), func(p string, e fs.DirEntry, err error) error {
		if err != nil || len(out) >= maxFiles {
			return filepath.SkipDir
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return nil // never follow links out of the folder
		}
		if !e.IsDir() {
			rel, _ := filepath.Rel(string(d), p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func (d dirBundle) read(name string) ([]byte, error) {
	clean := path.Clean("/" + name)[1:]
	if clean == "" || !slices.Contains(d.list(), clean) {
		return nil, fmt.Errorf("the skill has no file %s", name)
	}
	return os.ReadFile(filepath.Join(string(d), filepath.FromSlash(clean)))
}

// openZip checks a .skill file and finds its SKILL.md: at the root, or in
// the one folder the zip was made from.
func openZip(data []byte) (*zip.Reader, string, error) {
	if len(data) > maxZip {
		return nil, "", fmt.Errorf("%w: a .skill file is at most 20 MB", apperr.Invalid)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("%w: not a .skill (zip) file: %v", apperr.Invalid, err)
	}
	if len(r.File) > maxFiles {
		return nil, "", fmt.Errorf("%w: a skill has at most %d files", apperr.Invalid, maxFiles)
	}
	var total uint64
	prefix := ""
	found := false
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") || slices.Contains(strings.Split(f.Name, "/"), "..") {
			return nil, "", fmt.Errorf("%w: unsafe path %q in the skill", apperr.Invalid, f.Name)
		}
		total += f.UncompressedSize64
		if base := path.Base(f.Name); base == "SKILL.md" && strings.Count(f.Name, "/") <= 1 && !found {
			prefix, found = strings.TrimSuffix(f.Name, "SKILL.md"), true
		}
	}
	if total > maxTotal {
		return nil, "", fmt.Errorf("%w: the skill unpacks to more than 60 MB", apperr.Invalid)
	}
	if !found {
		return nil, "", fmt.Errorf("%w: no SKILL.md at the top of the .skill file (or in its one folder)", apperr.Invalid)
	}
	return r, prefix, nil
}

// ---- workspace skills ------------------------------------------------------------

func (s *Service) zipPath(ws, name string) string {
	return filepath.Join(s.dir, ws, name+".skill")
}

type UploadRequest struct {
	WorkspaceID string `json:"workspaceId"`
	FileName    string `json:"fileName"`
	Data        []byte `json:"data"` // base64 in JSON
}

// Upload adds a workspace skill from a .skill file (replacing one with the
// same name), enabled.
func (s *Service) Upload(req UploadRequest) (Skill, error) {
	r, prefix, err := openZip(req.Data)
	if err != nil {
		return Skill{}, err
	}
	md, err := (zipBundle{r, prefix}).read("SKILL.md")
	if err != nil {
		return Skill{}, err
	}
	name, desc, err := frontMatter(string(md))
	if err != nil {
		return Skill{}, fmt.Errorf("%w: %v", apperr.Invalid, err)
	}
	if err := os.MkdirAll(filepath.Join(s.dir, req.WorkspaceID), 0o700); err != nil {
		return Skill{}, err
	}
	tmp := s.zipPath(req.WorkspaceID, name) + ".tmp"
	if err := os.WriteFile(tmp, req.Data, 0o600); err != nil {
		return Skill{}, err
	}
	if err := os.Rename(tmp, s.zipPath(req.WorkspaceID, name)); err != nil {
		return Skill{}, err
	}
	sum := sha256.Sum256(req.Data)
	files := len((zipBundle{r, prefix}).list())
	if _, err := s.db.Exec(`INSERT INTO skills (workspace_id, name, description, enabled, prefix, size, files, sha, updated_at) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id, name) DO UPDATE SET description = excluded.description, prefix = excluded.prefix, size = excluded.size,
		files = excluded.files, sha = excluded.sha, updated_at = excluded.updated_at`,
		req.WorkspaceID, name, desc, prefix, len(req.Data), files, hex.EncodeToString(sum[:]), sqlitedb.Stamp(s.now())); err != nil {
		return Skill{}, err
	}
	s.changed(req.WorkspaceID, name)
	return Skill{Name: name, Description: desc, Source: "workspace", Enabled: true, Files: files, Size: int64(len(req.Data)), UpdatedAt: s.now()}, nil
}

func (s *Service) workspaceSkills(ws string) ([]Skill, error) {
	rows, err := s.db.Query(`SELECT name, description, enabled, size, files, updated_at FROM skills WHERE workspace_id = ? ORDER BY name`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		k := Skill{Source: "workspace"}
		var at string
		if err := rows.Scan(&k.Name, &k.Description, &k.Enabled, &k.Size, &k.Files, &at); err != nil {
			return nil, err
		}
		k.UpdatedAt = sqlitedb.ParseStamp(at)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Service) workspaceBundle(ws, name string) (bundle, string, error) {
	var prefix, sha string
	if err := s.db.QueryRow(`SELECT prefix, sha FROM skills WHERE workspace_id = ? AND name = ?`, ws, name).Scan(&prefix, &sha); err != nil {
		return nil, "", fmt.Errorf("%w: skill %s", apperr.NotFound, name)
	}
	data, err := os.ReadFile(s.zipPath(ws, name))
	if err != nil {
		return nil, "", err
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", err
	}
	return zipBundle{r, prefix}, sha, nil
}

// ---- project skills -----------------------------------------------------------

// projectSkills finds the skills in a project's folder.
func (s *Service) projectSkills(ws string, p project.Project) []Skill {
	toggles := map[string]bool{}
	rows, err := s.db.Query(`SELECT name, enabled FROM skill_toggles WHERE workspace_id = ? AND project_id = ?`, ws, p.ID)
	if err == nil {
		for rows.Next() {
			var n string
			var on bool
			if rows.Scan(&n, &on) == nil {
				toggles[n] = on
			}
		}
		rows.Close()
	}
	out := []Skill{}
	seen := map[string]bool{}
	for _, rel := range ProjectDirs {
		entries, err := os.ReadDir(filepath.Join(p.Path, rel))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			folder := filepath.Join(p.Path, rel, e.Name())
			md, err := os.ReadFile(filepath.Join(folder, "SKILL.md"))
			if err != nil {
				continue
			}
			name, desc, err := frontMatter(string(md))
			if err != nil || seen[name] {
				continue
			}
			seen[name] = true
			on, set := toggles[name]
			files := dirBundle(folder).list()
			var size int64
			for _, f := range files {
				if fi, err := os.Stat(filepath.Join(folder, f)); err == nil {
					size += fi.Size()
				}
			}
			fi, _ := os.Stat(filepath.Join(folder, "SKILL.md"))
			k := Skill{Name: name, Description: desc, Source: "project", ProjectID: p.ID, Folder: filepath.ToSlash(filepath.Join(rel, e.Name())),
				Enabled: !set || on, Files: len(files), Size: size}
			if fi != nil {
				k.UpdatedAt = fi.ModTime()
			}
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) projects(ws string) ([]project.Project, error) {
	return project.NewService(s.db).Projects(ws)
}

// ---- owner API -----------------------------------------------------------------

// Skills lists the workspace's skills and each project's.
func (s *Service) Skills(workspaceID string) (Listing, error) {
	ws, err := s.workspaceSkills(workspaceID)
	if err != nil {
		return Listing{}, err
	}
	list := Listing{Workspace: ws, Projects: []ProjectSkills{}}
	projects, err := s.projects(workspaceID)
	if err != nil {
		return Listing{}, err
	}
	for _, p := range projects {
		if found := s.projectSkills(workspaceID, p); len(found) > 0 {
			list.Projects = append(list.Projects, ProjectSkills{ProjectID: p.ID, Project: p.Name, Skills: found})
		}
	}
	return list, nil
}

type ToggleRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ProjectID   int64  `json:"projectId"` // 0: a workspace skill
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
}

// SetEnabled switches a skill on or off.
func (s *Service) SetEnabled(req ToggleRequest) error {
	if req.ProjectID == 0 {
		res, err := s.db.Exec(`UPDATE skills SET enabled = ? WHERE workspace_id = ? AND name = ?`, req.Enabled, req.WorkspaceID, req.Name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: skill %s", apperr.NotFound, req.Name)
		}
	} else if _, err := s.db.Exec(`INSERT INTO skill_toggles (workspace_id, project_id, name, enabled) VALUES (?, ?, ?, ?)
		ON CONFLICT (workspace_id, project_id, name) DO UPDATE SET enabled = excluded.enabled`, req.WorkspaceID, req.ProjectID, req.Name, req.Enabled); err != nil {
		return err
	}
	s.changed(req.WorkspaceID, req.Name)
	return nil
}

type NameRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
}

// Delete removes a workspace skill.
func (s *Service) Delete(req NameRequest) error {
	res, err := s.db.Exec(`DELETE FROM skills WHERE workspace_id = ? AND name = ?`, req.WorkspaceID, req.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: skill %s", apperr.NotFound, req.Name)
	}
	os.Remove(s.zipPath(req.WorkspaceID, req.Name))
	s.changed(req.WorkspaceID, req.Name)
	return nil
}

type ExportRequest struct {
	WorkspaceID string `json:"workspaceId"`
	ProjectID   int64  `json:"projectId"` // 0: a workspace skill
	Name        string `json:"name"`
}

type File struct {
	Name string `json:"name"`
	Data []byte `json:"data"` // base64 in JSON
}

// Export returns a skill as a .skill file: a workspace skill as uploaded, a
// project skill zipped from its folder (the folder becomes the zip's one
// top folder, as Claude's skill uploads expect).
func (s *Service) Export(req ExportRequest) (File, error) {
	if req.ProjectID == 0 {
		data, err := os.ReadFile(s.zipPath(req.WorkspaceID, req.Name))
		if err != nil {
			return File{}, fmt.Errorf("%w: skill %s", apperr.NotFound, req.Name)
		}
		return File{Name: req.Name + ".skill", Data: data}, nil
	}
	p, err := project.Get(s.db, req.WorkspaceID, req.ProjectID)
	if err != nil {
		return File{}, err
	}
	for _, k := range s.projectSkills(req.WorkspaceID, p) {
		if k.Name != req.Name {
			continue
		}
		folder := dirBundle(filepath.Join(p.Path, filepath.FromSlash(k.Folder)))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, f := range folder.list() {
			b, err := folder.read(f)
			if err != nil {
				return File{}, err
			}
			w, err := zw.Create(k.Name + "/" + f)
			if err != nil {
				return File{}, err
			}
			w.Write(b)
		}
		if err := zw.Close(); err != nil {
			return File{}, err
		}
		return File{Name: k.Name + ".skill", Data: buf.Bytes()}, nil
	}
	return File{}, fmt.Errorf("%w: skill %s in project #%d", apperr.NotFound, req.Name, req.ProjectID)
}

func (s *Service) changed(ws, name string) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	ev, err := eventlog.Append(tx, ws, 0, EventChanged, map[string]string{"name": name}, s.now())
	if err == nil && tx.Commit() == nil {
		s.publish(ev)
	}
}

// ---- agent side ----------------------------------------------------------------

// visible is a skill an agent can use, with where its files are.
type visible struct {
	Skill
	bundle bundle
	folder string // a project skill's folder on disk
	sha    string // a workspace skill's content hash
}

// forAgent lists the enabled skills an agent in projectID can use: its
// project's, then the workspace's (a project skill wins a name clash).
func (s *Service) forAgent(ws string, projectID int64) []visible {
	var out []visible
	seen := map[string]bool{}
	if projectID != 0 {
		if p, err := project.Get(s.db, ws, projectID); err == nil {
			for _, k := range s.projectSkills(ws, p) {
				if k.Enabled {
					folder := filepath.Join(p.Path, filepath.FromSlash(k.Folder))
					out = append(out, visible{Skill: k, bundle: dirBundle(folder), folder: folder})
					seen[k.Name] = true
				}
			}
		}
	}
	list, _ := s.workspaceSkills(ws)
	for _, k := range list {
		if k.Enabled && !seen[k.Name] {
			out = append(out, visible{Skill: k})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) find(ws string, projectID int64, name string) (visible, error) {
	for _, v := range s.forAgent(ws, projectID) {
		if v.Name != name {
			continue
		}
		if v.bundle == nil {
			b, sha, err := s.workspaceBundle(ws, name)
			if err != nil {
				return v, err
			}
			v.bundle, v.sha = b, sha
		}
		return v, nil
	}
	return visible{}, fmt.Errorf("there is no enabled skill named %q (the skills you can use are listed in your instructions)", name)
}

// Index is the skills section of an agent's system prompt ("" for none).
func (s *Service) Index(ws string, projectID int64) string {
	list := s.forAgent(ws, projectID)
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills: instructions for particular kinds of work. When a task matches one, load it with skill_read before starting, and follow it. Its files are read with skill_read(name, file); to run its scripts, name it in bash's `skills` and use $SKILL_<NAME> (its folder, read-only).\n")
	for _, k := range list {
		fmt.Fprintf(&b, "- %s: %s\n", k.Name, k.Description)
	}
	return strings.TrimSpace(b.String())
}

// Read returns a skill's SKILL.md with the list of its other files, or one
// of its files (text only, clipped).
func (s *Service) Read(ws string, projectID int64, name, file string) (string, error) {
	v, err := s.find(ws, projectID, name)
	if err != nil {
		return "", err
	}
	if file == "" || file == "SKILL.md" {
		md, err := v.bundle.read("SKILL.md")
		if err != nil {
			return "", err
		}
		var others []string
		for _, f := range v.bundle.list() {
			if f != "SKILL.md" {
				others = append(others, f)
			}
		}
		out := clip(string(md))
		if len(others) > 0 {
			out += "\n\n---\nFiles in this skill (read one with skill_read(" + fmt.Sprintf("%q", name) + ", file)):\n- " + strings.Join(others, "\n- ")
		}
		return out, nil
	}
	data, err := v.bundle.read(strings.TrimPrefix(path.Clean("/"+file), "/"))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("%s is a binary file (%d bytes). To use it, name the skill in bash's `skills` and use it from $SKILL_%s.", file, len(data), EnvName(name)), nil
	}
	return clip(string(data)), nil
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxRead {
		return s
	}
	r := []rune(s)
	return string(r[:maxRead]) + "\n…[cut: the file is longer]"
}

// EnvName is a skill's environment variable suffix: SKILL_<NAME>.
func EnvName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Dirs gives a command the folders of the named skills: a project skill's
// own folder, or a workspace skill unpacked (once per version) beside the
// .skill files. The sandbox makes them readable, not writable.
func (s *Service) Dirs(ws string, projectID int64, names []string) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range names {
		v, err := s.find(ws, projectID, name)
		if err != nil {
			return nil, err
		}
		if v.folder != "" {
			out[name] = v.folder
			continue
		}
		dir := filepath.Join(s.dir, "unpacked", v.sha)
		if _, err := os.Stat(dir); err != nil {
			if err := unpack(v.bundle, dir); err != nil {
				return nil, fmt.Errorf("unpacking skill %s: %w", name, err)
			}
		}
		out[name] = dir
	}
	return out, nil
}

func unpack(b bundle, dir string) error {
	tmp := dir + ".tmp"
	os.RemoveAll(tmp)
	for _, f := range b.list() {
		data, err := b.read(f)
		if err != nil {
			return err
		}
		dst := filepath.Join(tmp, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if bytes.HasPrefix(data, []byte("#!")) {
			mode = 0o755 // scripts stay runnable
		}
		if err := os.WriteFile(dst, data, mode); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dir)
}
