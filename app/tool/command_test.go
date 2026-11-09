package tool

import "testing"

// Each read command with the flags that make it write a file or run another
// program is exec, so it waits for the owner. The controls next to them stay
// read: the same command without the flag, or with a flag that only reads.
func TestReadCommandsThatWriteOrRunAreExec(t *testing.T) {
	exec := []string{
		"sort -o out.txt a.txt",
		"sort -ro out a",
		"sort --output=out a",
		"sort --out out a",
		"sort --compress-program=sh a",
		"sort --compress-program sh a",
		"tree -o out.txt",
		"tree -oR",
		"tree -R",
		"file -C -m magic",
		"file -bC x",
		"file --compile x",
		"file --magic-file=m x",
		"rg --pre sh foo",
		"rg --pre=sh foo",
		"rg --pre-glob '*.gz' --pre sh foo",
		"rg --hostname-bin sh foo",
		"uniq in out",
		"uniq -c a b",
		"uniq -- -a b",
		"git diff --textconv",
		"git log --textconv -p",
		"git show --ext-diff HEAD",
		"git diff --output=x",
	}
	for _, c := range exec {
		if got := ClassifyCommand(c); got != Exec {
			t.Errorf("ClassifyCommand(%q) = %s, want exec", c, got)
		}
	}
	read := []string{
		"sort -n a",
		"sort -k 2 -t , a",
		"tail -f log",
		"tree -L 2",
		"tree -I node_modules",
		"rg -n foo",
		"rg --pretty foo",
		"uniq -c a",
		"uniq a",
		"uniq -- a",
		"file -b x",
		"git diff --stat",
		"git log -1 --oneline",
		"git show HEAD --stat",
		"git diff --no-ext-diff",
	}
	for _, c := range read {
		if got := ClassifyCommand(c); got != Read {
			t.Errorf("ClassifyCommand(%q) = %s, want read", c, got)
		}
	}
}

// ReadWords is ClassifyCommand's read class for one simple command.
func TestReadWords(t *testing.T) {
	read := [][]string{
		{"echo", "ok"},
		{"ls", "-la"},
		{"cat", "go.mod"},
		{"grep", "-r", "foo", "app"},
		{"git", "status"}, // the gate's read class; the list leaves git out (see readPart)
	}
	for _, w := range read {
		if !ReadWords(w) {
			t.Errorf("ReadWords(%q) = false; want true", w)
		}
	}
	notRead := [][]string{
		nil,
		{"sort", "-o", "out.txt", "a"},
		{"cat", "/etc/passwd"},
		{"go", "build", "./..."},
		{"rm", "x"},
	}
	for _, w := range notRead {
		if ReadWords(w) {
			t.Errorf("ReadWords(%q) = true; want false", w)
		}
	}
}
