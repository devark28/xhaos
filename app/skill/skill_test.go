package skill_test

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xhaos/app/backend"
	"xhaos/app/skill"
)

const ws = "default"

func open(t *testing.T) (*backend.Services, string) {
	t.Helper()
	root := t.TempDir()
	s, err := backend.Open(filepath.Join(t.TempDir(), "x.db"), backend.Options{Root: root, DisableRuns: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, root
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const md = "---\nname: release-notes\ndescription: >\n  Write release notes from the git log,\n  grouped by area.\n---\n# Release notes\nRun scripts/collect.sh first.\n"

func TestWorkspaceSkills(t *testing.T) {
	s, _ := open(t)
	// Zipped from its folder (the usual way), with a script.
	data := zipOf(t, map[string]string{"release-notes/SKILL.md": md, "release-notes/scripts/collect.sh": "#!/bin/sh\necho collected\n", "release-notes/template.md": "## {{area}}"})
	k, err := s.Skills.Upload(skill.UploadRequest{WorkspaceID: ws, FileName: "release-notes.skill", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if k.Name != "release-notes" || k.Description != "Write release notes from the git log, grouped by area." || k.Files != 3 || !k.Enabled {
		t.Fatalf("skill = %+v", k)
	}
	if idx := s.Skills.Index(ws, 0); !strings.Contains(idx, "- release-notes: Write release notes") {
		t.Fatalf("index = %q", idx)
	}
	out, err := s.Skills.Read(ws, 0, "release-notes", "")
	if err != nil || !strings.Contains(out, "Run scripts/collect.sh first.") || !strings.Contains(out, "- scripts/collect.sh") || !strings.Contains(out, "- template.md") {
		t.Fatalf("read = %q %v", out, err)
	}
	if out, _ := s.Skills.Read(ws, 0, "release-notes", "template.md"); out != "## {{area}}" {
		t.Fatalf("file = %q", out)
	}
	if _, err := s.Skills.Read(ws, 0, "release-notes", "../../etc/passwd"); err == nil {
		t.Fatal("read outside the skill")
	}
	dirs, err := s.Skills.Dirs(ws, 0, []string{"release-notes"})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dirs["release-notes"], "scripts", "collect.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("unpacked script = %v %v", fi, err)
	}
	// Off: gone from the index and from skill_read; on again: back.
	s.Skills.SetEnabled(skill.ToggleRequest{WorkspaceID: ws, Name: "release-notes", Enabled: false})
	if idx := s.Skills.Index(ws, 0); idx != "" {
		t.Fatalf("disabled skill listed: %q", idx)
	}
	if _, err := s.Skills.Read(ws, 0, "release-notes", ""); err == nil {
		t.Fatal("read a disabled skill")
	}
	s.Skills.SetEnabled(skill.ToggleRequest{WorkspaceID: ws, Name: "release-notes", Enabled: true})
	exported, err := s.Skills.Export(skill.ExportRequest{WorkspaceID: ws, Name: "release-notes"})
	if err != nil || !bytes.Equal(exported.Data, data) {
		t.Fatalf("export = %v", err)
	}
	if err := s.Skills.Delete(skill.NameRequest{WorkspaceID: ws, Name: "release-notes"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Skills.Skills(ws); len(l.Workspace) != 0 {
		t.Fatalf("after delete: %+v", l.Workspace)
	}
	// SKILL.md at the zip's root works too.
	if _, err := s.Skills.Upload(skill.UploadRequest{WorkspaceID: ws, Data: zipOf(t, map[string]string{"SKILL.md": "---\nname: tiny\ndescription: \"A tiny one.\"\n---\nhi"})}); err != nil {
		t.Fatal(err)
	}
}

func TestBadSkillFiles(t *testing.T) {
	s, _ := open(t)
	bad := map[string][]byte{
		"not a zip":          []byte("hello"),
		"no SKILL.md":        zipOf(t, map[string]string{"x/readme.md": "hi"}),
		"no front matter":    zipOf(t, map[string]string{"SKILL.md": "# just text"}),
		"no description":     zipOf(t, map[string]string{"SKILL.md": "---\nname: x\n---\n"}),
		"bad name":           zipOf(t, map[string]string{"SKILL.md": "---\nname: Bad Name\ndescription: d\n---\n"}),
		"a path out of it":   zipOf(t, map[string]string{"SKILL.md": md, "../evil.sh": "rm -rf /"}),
		"deep SKILL.md only": zipOf(t, map[string]string{"a/b/SKILL.md": md}),
	}
	for what, data := range bad {
		if _, err := s.Skills.Upload(skill.UploadRequest{WorkspaceID: ws, Data: data}); err == nil {
			t.Errorf("accepted %s", what)
		}
	}
}

func TestProjectSkills(t *testing.T) {
	s, root := open(t)
	dir := filepath.Join(root, ".claude", "skills", "deploy")
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: deploy\ndescription: Deploy the site to staging.\n---\nRun bin/go.sh."), 0o644)
	os.WriteFile(filepath.Join(dir, "bin", "go.sh"), []byte("#!/bin/sh\necho deployed\n"), 0o755)
	// A link out of the folder is not part of the skill.
	os.Symlink("/etc/passwd", filepath.Join(dir, "passwd"))
	list, err := s.Skills.Skills(ws)
	if err != nil || len(list.Projects) != 1 || len(list.Projects[0].Skills) != 1 {
		t.Fatalf("listing = %+v %v", list, err)
	}
	k := list.Projects[0].Skills[0]
	pid := list.Projects[0].ProjectID
	if k.Name != "deploy" || k.Folder != ".claude/skills/deploy" || !k.Enabled || k.Files != 2 {
		t.Fatalf("project skill = %+v", k)
	}
	if idx := s.Skills.Index(ws, pid); !strings.Contains(idx, "- deploy: Deploy the site") {
		t.Fatalf("index for the project's agents = %q", idx)
	}
	if idx := s.Skills.Index(ws, pid+1); strings.Contains(idx, "deploy") {
		t.Fatal("another project's agent sees it")
	}
	if _, err := s.Skills.Read(ws, pid, "deploy", "passwd"); err == nil {
		t.Fatal("read through a link")
	}
	if dirs, _ := s.Skills.Dirs(ws, pid, []string{"deploy"}); dirs["deploy"] != dir {
		t.Fatalf("project skill folder = %v", dirs)
	}
	s.Skills.SetEnabled(skill.ToggleRequest{WorkspaceID: ws, ProjectID: pid, Name: "deploy", Enabled: false})
	if idx := s.Skills.Index(ws, pid); idx != "" {
		t.Fatalf("switched off, still listed: %q", idx)
	}
	// Export, then upload as a workspace skill: the same skill.
	f, err := s.Skills.Export(skill.ExportRequest{WorkspaceID: ws, ProjectID: pid, Name: "deploy"})
	if err != nil || f.Name != "deploy.skill" {
		t.Fatalf("export = %v %v", f.Name, err)
	}
	up, err := s.Skills.Upload(skill.UploadRequest{WorkspaceID: ws, Data: f.Data})
	if err != nil || up.Name != "deploy" || up.Files != 2 {
		t.Fatalf("re-import = %+v %v", up, err)
	}
}
