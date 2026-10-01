package toggle

import (
	"os"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestMissingFileMeansEverythingOn(t *testing.T) {
	f, err := Load(t.TempDir())
	if err != nil || f.Off("commit", "/x", t0) {
		t.Fatalf("err %v, off %v", err, f.Off("commit", "/x", t0))
	}
}

func TestDisableThenEnable(t *testing.T) {
	dir := t.TempDir()
	if err := Disable(dir, []string{"commit"}, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	if !f.Off("commit", "/any", t0) || f.Off("gh", "/any", t0) {
		t.Fatal("commit should be off and gh on")
	}
	if err := Enable(dir, []string{"commit"}, t0); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(dir)
	if f.Off("commit", "/any", t0) {
		t.Fatal("commit still off after enable")
	}
	if _, err := os.Stat(Path(dir)); !os.IsNotExist(err) {
		t.Fatalf("empty state should remove the file, got %v", err)
	}
}

func TestExpires(t *testing.T) {
	dir := t.TempDir()
	Disable(dir, []string{"commit"}, t0.Add(time.Hour), nil)
	f, _ := Load(dir)
	if !f.Off("commit", "", t0.Add(59*time.Minute)) {
		t.Fatal("should be off before the deadline")
	}
	if f.Off("commit", "", t0.Add(time.Hour)) {
		t.Fatal("should be on again at the deadline")
	}
}

func TestDirectoryScope(t *testing.T) {
	dir := t.TempDir()
	Disable(dir, []string{"commit"}, time.Time{}, []string{"/work/internal"})
	f, _ := Load(dir)
	for cwd, want := range map[string]bool{
		"/work/internal":          true,
		"/work/internal/sub/deep": true,
		"/work/internal-other":    false,
		"/work":                   false,
		"/work/internal/../x":     false,
		"":                        false,
	} {
		if got := f.Off("commit", cwd, t0); got != want {
			t.Errorf("cwd %q: off=%v want %v", cwd, got, want)
		}
	}
}

func TestDamagedFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(Path(dir), []byte("{nope"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("want an error")
	}
	// the user can still repair it with disable or enable
	if err := Enable(dir, []string{"commit"}, t0); err != nil {
		t.Fatal(err)
	}
}

func TestExpand(t *testing.T) {
	got, err := Expand([]string{"all"})
	if err != nil || len(got) != len(Kinds) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := Expand([]string{"commits"}); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("err %v", err)
	}
	if _, err := Expand(nil); err == nil {
		t.Fatal("want an error for no kinds")
	}
}

func TestFileMode(t *testing.T) {
	dir := t.TempDir()
	Disable(dir, []string{"email"}, time.Time{}, nil)
	st, _ := os.Stat(Path(dir))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
}

func TestLines(t *testing.T) {
	dir := t.TempDir()
	Disable(dir, []string{"commit"}, t0.Add(90*time.Minute), []string{"/work/internal"})
	f, _ := Load(dir)
	out := strings.Join(f.Lines(t0), "\n")
	if !strings.Contains(out, "commit   OFF") || !strings.Contains(out, "/work/internal") || !strings.Contains(out, "gh       on") {
		t.Fatalf("\n%s", out)
	}
}
