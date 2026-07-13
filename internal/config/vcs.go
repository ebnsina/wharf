package config

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// IsTracked reports whether a file is committed to git.
//
// wharf writes a service's berth into that service's own config. That is safe
// when the config is a gitignored local file — most projects treat it that way,
// since it holds machine-specific values. But some commit it, and for those the
// same write modifies a file every teammate shares: a local port choice would
// land in a pull request, or worse, be pushed without anyone noticing.
//
// So tracked configs are never written unless explicitly forced. A false
// negative (git absent, not a repo) errs toward "not tracked", which is the same
// answer wharf gives for any project outside version control.
func IsTracked(path string) bool {
	dir := filepath.Dir(path)

	cmd := exec.Command("git", "-C", dir, "ls-files", "--error-unmatch", filepath.Base(path))
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// IsIgnored reports whether git is explicitly ignoring a file. Used only to
// distinguish "ignored" from "untracked but not ignored" in diagnostics.
func IsIgnored(path string) bool {
	dir := filepath.Dir(path)

	cmd := exec.Command("git", "-C", dir, "check-ignore", "-q", filepath.Base(path))
	return cmd.Run() == nil
}

// ErrTracked is returned when a write would modify a version-controlled file.
type ErrTracked struct {
	Service string
	File    string
}

func (e *ErrTracked) Error() string {
	return e.Service + ": " + shorten(e.File) + " is committed to git; " +
		"writing a berth into it would change a file your teammates share"
}

func shorten(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) <= 3 {
		return path
	}
	return filepath.Join(parts[len(parts)-3:]...)
}
