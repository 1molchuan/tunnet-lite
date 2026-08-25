package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// inTempDir runs the test from a scratch working directory, since resolution
// deliberately consults it.
func inTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
	return dir
}

// withConfigDir redirects the per-user configuration directory for one test.
func withConfigDir(t *testing.T, dir string) {
	t.Helper()
	previous := userConfigDir
	userConfigDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { userConfigDir = previous })
}

func TestAnExplicitDirectoryWins(t *testing.T) {
	inTempDir(t)
	t.Setenv(HomeEnv, filepath.Join("should", "be", "ignored"))

	want := t.TempDir()
	p, err := Resolve(want)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.Identity) != want {
		t.Errorf("identity = %q, want it under %q", p.Identity, want)
	}
}

func TestTheEnvironmentOverridesTheDefault(t *testing.T) {
	inTempDir(t)
	want := t.TempDir()
	t.Setenv(HomeEnv, want)

	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p.Nodes) != want {
		t.Errorf("nodes = %q, want it under %q", p.Nodes, want)
	}
}

// Running from a different directory used to mean starting from nothing, so an
// untouched install must land somewhere stable and absolute.
func TestTheDefaultIsAbsoluteAndOutsideTheWorkingDirectory(t *testing.T) {
	work := inTempDir(t)
	t.Setenv(HomeEnv, "")

	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"nodes": p.Nodes, "identity": p.Identity, "pins": p.Pins, "state": p.State,
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s = %q, want an absolute path", name, path)
		}
		if filepath.Dir(path) == work {
			t.Errorf("%s landed in the working directory", name)
		}
	}
	if p.UsingLegacy() {
		t.Error("a clean directory should not be reported as legacy")
	}
}

// An existing setup must keep working: adopting the new location silently would
// abandon the authorisation attached to the old identity.
func TestAFileLeftByAnEarlierVersionIsStillUsed(t *testing.T) {
	inTempDir(t)
	t.Setenv(HomeEnv, "")
	withConfigDir(t, t.TempDir())

	if err := os.WriteFile("tunnet-lite-identity.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != "tunnet-lite-identity.json" {
		t.Errorf("identity = %q, want the file already in the working directory", p.Identity)
	}
	if !p.UsingLegacy() {
		t.Error("UsingLegacy should report that something is being read from the working directory")
	}
	// Files that were not left behind still resolve to the new location.
	if !filepath.IsAbs(p.Nodes) {
		t.Errorf("nodes = %q, want the new location", p.Nodes)
	}
}

// A file already present in the new location takes precedence, so a stale
// leftover cannot shadow the current one.
func TestTheNewLocationWinsWhenBothExist(t *testing.T) {
	inTempDir(t)
	t.Setenv(HomeEnv, "")
	home := filepath.Join(t.TempDir(), appDir)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	withConfigDir(t, filepath.Dir(home))

	if err := os.WriteFile("tunnet-lite-identity.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "identity.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != filepath.Join(home, "identity.json") {
		t.Errorf("identity = %q, want the one in the configured directory", p.Identity)
	}
}

func TestEnsureDirCreatesAnOwnerOnlyDirectory(t *testing.T) {
	inTempDir(t)
	dir := filepath.Join(t.TempDir(), "nested", "home")

	p, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
}

// An explicit directory is a statement of intent: a file that happens to sit in
// the working directory must not quietly take precedence over it.
func TestAnExplicitDirectoryIgnoresLegacyFiles(t *testing.T) {
	inTempDir(t)
	if err := os.WriteFile("tunnet-lite-identity.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	want := t.TempDir()
	p, err := Resolve(want)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != filepath.Join(want, "identity.json") {
		t.Errorf("identity = %q, want it under the directory that was asked for", p.Identity)
	}
	if p.UsingLegacy() {
		t.Error("an explicit directory should never be reported as legacy")
	}
}

func TestTheEnvironmentAlsoIgnoresLegacyFiles(t *testing.T) {
	inTempDir(t)
	if err := os.WriteFile("nodes.json", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := t.TempDir()
	t.Setenv(HomeEnv, want)

	p, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Nodes != filepath.Join(want, "nodes.json") {
		t.Errorf("nodes = %q, want it under %q", p.Nodes, want)
	}
}
