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
//
// Commands can write the project, so its .git file is not trusted: a
// command could point it at the node's key or ~/.ssh and have the next
// command get that folder writable. A folder outside the project is taken
// only when it is git's own and names this project back: a worktree's
// folder (<repo>/worktrees/<name>) whose gitdir file is this project's
// .git, or a submodule's (…/modules/<name>) whose core.worktree is this
// project. A commondir must be the worktree folder's repository
// (<repo>/worktrees/<name> → <repo>) or inside the project. A .git that is
// a symlink, and anything else, gives nothing.
func GitDirs(root string) []string {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	root = real
	dot := filepath.Join(root, ".git")
	fi, err := os.Lstat(dot)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if fi.IsDir() {
		return []string{dot}
	}
	if !fi.Mode().IsRegular() {
		return nil
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(readSmall(dot)), "gitdir:")
	if !ok {
		return nil
	}
	dir := resolveDir(root, strings.TrimSpace(target))
	if dir == "" || (!within(dir, root) && !worktreeOf(dir, dot) && !submoduleOf(dir, root)) {
		return nil
	}
	out := []string{dir}
	if c := readSmall(filepath.Join(dir, "commondir")); c != "" {
		common := resolveDir(dir, strings.TrimSpace(c))
		parent := filepath.Dir(dir)
		if common != "" && (within(common, root) || (filepath.Base(parent) == "worktrees" && filepath.Dir(parent) == common)) {
			out = append(out, common)
		}
	}
	return out
}

// resolveDir is path (relative to base when not absolute) with its links
// resolved, when it is a folder; else "".
func resolveDir(base, path string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return ""
	}
	return real
}

// within: path is dir or inside it (both clean and resolved).
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// worktreeOf: dir is a worktree's folder in its repository, and its gitdir
// file names dot (the worktree's .git file).
func worktreeOf(dir, dot string) bool {
	if filepath.Base(filepath.Dir(dir)) != "worktrees" {
		return false
	}
	back := strings.TrimSpace(readSmall(filepath.Join(dir, "gitdir")))
	if back == "" {
		return false
	}
	if !filepath.IsAbs(back) {
		back = filepath.Join(dir, back)
	}
	real, err := filepath.EvalSymlinks(back)
	return err == nil && real == dot
}

// submoduleOf: dir is a submodule's folder (…/modules/<name>, names may
// nest) whose config sets core.worktree to root.
func submoduleOf(dir, root string) bool {
	if !strings.Contains(dir, string(filepath.Separator)+"modules"+string(filepath.Separator)) {
		return false
	}
	section := ""
	for _, line := range strings.Split(readSmall(filepath.Join(dir, "config")), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[] \t"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "core" || !strings.EqualFold(strings.TrimSpace(key), "worktree") {
			continue
		}
		wt := strings.Trim(strings.TrimSpace(value), `"`)
		if !filepath.IsAbs(wt) {
			wt = filepath.Join(dir, wt)
		}
		real, err := filepath.EvalSymlinks(wt)
		return err == nil && real == root
	}
	return false
}

// readSmall reads a small regular file (a .git file, gitdir, commondir, a
// config), without following a final symlink; "" otherwise.
func readSmall(path string) string {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
