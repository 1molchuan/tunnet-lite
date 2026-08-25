// Package paths decides where tunnet-lite keeps its files.
//
// State used to land in the working directory, which meant running from
// somewhere else silently started from nothing and asked for authorisation
// again. Defaults now live in the per-user configuration directory, so the
// command works the same from anywhere.
package paths

import (
	"os"
	"path/filepath"
)

// HomeEnv overrides the base directory, which is useful for keeping separate
// identities side by side.
const HomeEnv = "TUNNET_LITE_HOME"

const appDir = "tunnet-lite"

// Paths is the resolved location of everything the client persists.
type Paths struct {
	Dir      string
	Nodes    string
	Identity string
	Pins     string
	State    string
	Assets   string
}

// legacy names are what earlier versions wrote into the working directory.
var legacy = map[string]string{
	"nodes":    "nodes.json",
	"identity": "tunnet-lite-identity.json",
	"pins":     "tunnet-lite-pins.json",
	"state":    "tunnet-lite-state.json",
	"assets":   "assets",
}

// Resolve returns the paths to use. An explicit dir wins; otherwise the
// per-user configuration directory is used, except that a file already present
// in the working directory keeps being used so an existing setup is not
// silently abandoned along with its authorisation.
func Resolve(dir string) (Paths, error) {
	if dir == "" {
		dir = os.Getenv(HomeEnv)
	}
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			// No configuration directory to speak of: the working directory is
			// a worse default but better than refusing to run.
			return withLegacy(Paths{Dir: "."}, true), nil
		}
		dir = filepath.Join(base, appDir)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	p := Paths{
		Dir:      abs,
		Nodes:    filepath.Join(abs, "nodes.json"),
		Identity: filepath.Join(abs, "identity.json"),
		Pins:     filepath.Join(abs, "pins.json"),
		State:    filepath.Join(abs, "state.json"),
		Assets:   filepath.Join(abs, "assets"),
	}
	return withLegacy(p, false), nil
}

// withLegacy points a field at the working directory when a file from an
// earlier version is there and the new location has nothing yet.
func withLegacy(p Paths, force bool) Paths {
	adopt := func(current, name string) string {
		old := legacy[name]
		if force {
			return old
		}
		if exists(old) && !exists(current) {
			return old
		}
		return current
	}
	p.Nodes = adopt(p.Nodes, "nodes")
	p.Identity = adopt(p.Identity, "identity")
	p.Pins = adopt(p.Pins, "pins")
	p.State = adopt(p.State, "state")
	if force {
		p.Assets = legacy["assets"]
	} else if exists(legacy["assets"]) && !exists(p.Assets) {
		p.Assets = legacy["assets"]
	}
	return p
}

// EnsureDir creates the base directory with owner-only permissions, since
// everything in it is a credential.
func (p Paths) EnsureDir() error {
	if p.Dir == "" || p.Dir == "." {
		return nil
	}
	return os.MkdirAll(p.Dir, 0o700)
}

// UsingLegacy reports whether any path still points at the working directory,
// so the caller can say so rather than leaving it a mystery.
func (p Paths) UsingLegacy() bool {
	for _, path := range []string{p.Nodes, p.Identity, p.Pins, p.State} {
		if !filepath.IsAbs(path) {
			return true
		}
	}
	return false
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
