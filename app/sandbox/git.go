package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// GitDirs is where a project's git data lives: its .git folder, or, when
// .git is a file (a submodule, or a worktree), the folder that file points
// to ("gitdir: ../.git/modules/name") and, for a worktree, the repository
// it belongs to (its commondir). These can be outside the project; the
// sandbox lets commands write them so git works, and nothing else of the
// parent repository.
func GitDirs(root string) []string {
	dot := filepath.Join(root, ".git")
	fi, err := os.Stat(dot)
	if err != nil {
		return nil
	}
	if fi.IsDir() {
		return []string{dot}
	}
	b, err := os.ReadFile(dot)
	if err != nil {
		return nil
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:")
	if !ok {
		return nil
	}
	dir := strings.TrimSpace(target)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Clean(dir)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil
	}
	out := []string{dir}
	if c, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		common := strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
		if fi, err := os.Stat(common); err == nil && fi.IsDir() {
			out = append(out, filepath.Clean(common))
		}
	}
	return out
}
