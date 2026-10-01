// Package config performs the file I/O that the profile package deliberately
// avoids.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.com/nighthawk-oss/saddle/internal/profile"
)

// ProfilesDir returns the profile directory, honouring XDG_CONFIG_HOME.
func ProfilesDir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "saddle", "profiles"), nil
}

// LoadProfiles reads every .yaml file in dir, sorted by profile name so
// detection is deterministic. A missing directory yields no profiles rather
// than an error, because a fresh install has none. A profile that fails to
// read or parse is skipped with a warning on stderr rather than aborting the
// whole load, so a typo in one unrelated profile does not block every
// `saddle up`.
func LoadProfiles(dir string) ([]profile.Profile, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []profile.Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "saddle: skipping invalid profile %s: %v\n", e.Name(), err)
			continue
		}
		p, err := profile.Parse(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "saddle: skipping invalid profile %s: %v\n", e.Name(), err)
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PresentFiles lists the names of entries in a repository root, which is what
// profile.Detect matches against.
func PresentFiles(repo string) ([]string, error) {
	entries, err := os.ReadDir(repo)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out, nil
}
