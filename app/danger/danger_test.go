package danger

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBranchLock(t *testing.T) {
	const b = "xhaos/7-fix-login"
	ok := []string{
		"go test ./...",
		"git status && git diff",
		"git add -A && git commit -m 'fix'",
		"git push",
		"git push origin",
		"git push -u origin " + b,
		"git push origin HEAD",
		"git push origin HEAD:" + b,
		"git push origin HEAD:refs/heads/" + b,
		"git switch " + b,
		"git checkout -- main.go",
		"git checkout HEAD -- main.go",
		"git branch",
		"git branch --show-current",
	}
	for _, c := range ok {
		if why := CheckCommand(c, b); why != "" {
			t.Errorf("%q refused: %s", c, why)
		}
	}
	refused := []string{
		"git switch main",
		"git checkout main",
		"git checkout -b other",
		"git switch -c other",
		"cd sub && git checkout develop",
		"git push --force",
		"git push -f origin " + b,
		"git push --force-with-lease",
		"git push origin main",
		"git push origin HEAD:main",
		"git push origin +" + b,
		"git push origin :" + b,
		"git push --delete origin " + b,
		"git push --all",
		"git push --mirror",
		"git push --tags",
		"git -C . push origin main",
		"echo hi; git push origin other",
		"git branch -D " + b,
		"git branch -m renamed",
	}
	for _, c := range refused {
		if why := CheckCommand(c, b); why == "" {
			t.Errorf("%q allowed", c)
		}
	}
	if why := CheckCommand("git push --force", ""); why != "" {
		t.Errorf("no branch (not a git project): %s", why)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	if b, err := CurrentBranch(dir); b != "" || err != nil {
		t.Fatalf("no repo = %q %v", b, err)
	}
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feature/x\n"), 0o644)
	if b, err := CurrentBranch(dir); b != "feature/x" || err != nil {
		t.Fatalf("branch = %q %v", b, err)
	}
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("3f2a9c0d\n"), 0o644)
	if _, err := CurrentBranch(dir); err == nil {
		t.Fatal("detached HEAD accepted")
	}
	if slug("Fix the Login page!! (urgent) and more words to cut") != "fix-the-login-page-urgent-and" || slug("!!!") != "work" {
		t.Fatalf("slug = %q", slug("Fix the Login page!! (urgent) and more words to cut"))
	}
}
