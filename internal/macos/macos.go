// Package macos answers one question: is this host new enough for saddle's
// containment to be the thing that was actually verified? The egress design
// rests on Apple `container` `--internal` network behaviour that was only
// ever checked on macOS 26, and saddle runs an agent with permission
// prompting disabled behind that isolation. Claiming support for an older OS
// where containment might silently not hold is worse than refusing to run.
package macos

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Floor is the lowest supported macOS major version.
const Floor = 26

// parseMajor extracts the major version from an `sw_vers -productVersion`
// string such as "26.6.2". A value it cannot understand is an error, never a
// pass: an unparseable version must not be mistaken for a supported one.
func parseMajor(productVersion string) (int, error) {
	s := strings.TrimSpace(productVersion)
	if s == "" {
		return 0, fmt.Errorf("empty macOS version")
	}
	major, _, _ := strings.Cut(s, ".")
	n, err := strconv.Atoi(major)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("unrecognised macOS version %q", s)
	}
	return n, nil
}

// Version returns the running macOS product version and its major number.
func Version() (string, int, error) {
	out, err := exec.Command("sw_vers", "-productVersion").Output()
	if err != nil {
		return "", 0, fmt.Errorf("read macOS version: %w", err)
	}
	v := strings.TrimSpace(string(out))
	major, err := parseMajor(v)
	if err != nil {
		return v, 0, err
	}
	return v, major, nil
}

// CheckFloor reports the running version and an error if this host is below
// the supported floor (or its version could not be determined).
func CheckFloor() (string, error) {
	v, major, err := Version()
	if err != nil {
		return v, err
	}
	if major < Floor {
		return v, fmt.Errorf("macOS %s is below saddle's floor of %d (Tahoe): saddle's containment relies on Apple `container` --internal network behaviour that was only verified on macOS %d and newer", v, Floor, Floor)
	}
	return v, nil
}
