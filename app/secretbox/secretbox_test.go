package secretbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealRoundTripAndKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	box, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal("sk-ant-test-1234")
	if err != nil || strings.Contains(sealed, "sk-ant") {
		t.Fatalf("sealed = %q, %v", sealed, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := reopened.Unseal(sealed); err != nil || plain != "sk-ant-test-1234" {
		t.Fatalf("Unseal = %q, %v", plain, err)
	}
	other, _ := Open(filepath.Join(t.TempDir(), "other.key"))
	if _, err := other.Unseal(sealed); err == nil {
		t.Fatal("a different key must not decrypt")
	}
}
