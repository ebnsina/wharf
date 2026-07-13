package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Backupper stores a pristine copy of a file before wharf first modifies it.
type Backupper interface {
	// Backup copies path into wharf's state directory, keyed by service.
	Backup(service, path string) error
}

// DirBackupper writes backups under a root directory, one subdirectory per
// service. Backups are timestamped and never overwritten, so a config can
// always be traced back to what it was before wharf touched it.
type DirBackupper struct {
	Root string
	// now is injectable so tests are not time-dependent.
	now func() time.Time
}

// NewDirBackupper returns a Backupper rooted at dir.
func NewDirBackupper(dir string) *DirBackupper {
	return &DirBackupper{Root: dir, now: time.Now}
}

func (b *DirBackupper) Backup(service, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	stamp := b.now().UTC().Format("20060102-150405")
	dst := filepath.Join(b.Root, service, stamp+"-"+filepath.Base(path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create backup dir: %w", err)
	}
	if err := os.WriteFile(dst, src, 0o644); err != nil {
		return fmt.Errorf("write backup %s: %w", dst, err)
	}
	return nil
}

// Change records one edit wharf made, for reporting back to the user. wharf
// modifies files inside the user's repos, so every write is accounted for
// rather than done silently.
type Change struct {
	Service string
	File    string
	Key     string
	From    string
	To      string
}

// PortPlaceholder is the token a port template substitutes.
const PortPlaceholder = "{port}"

// RenderPort writes a port in the shape the config field expects. A field
// holding ":8080" must receive ":8103", not "8103" — the latter is not a valid
// listen address and the service would fail to start.
//
// An empty template degrades to a bare number, which is the right default for a
// field that held one.
func RenderPort(template string, port int) string {
	if template == "" {
		return Itoa(port)
	}
	return strings.ReplaceAll(template, PortPlaceholder, Itoa(port))
}

// SetPort writes port into a service's config file at key, backing the file up
// first. It returns nil Change when the config already holds that port, so a
// repeated `wharf up` never dirties a file.
//
// path is absolute; key is a dotted path (YAML/JSON) or a variable name
// (dotenv); template is the value shape, e.g. ":{port}".
func SetPort(bk Backupper, service, path string, format Format, key, template string, port int, dryRun, force bool) (*Change, error) {
	if key == "" {
		return nil, fmt.Errorf("%s: no port key known for %s", service, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	before, _ := currentValue(raw, format, key)
	want := RenderPort(template, port)

	out, changed, err := SetValue(raw, format, key, want)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return nil, nil
	}

	// A config committed to git is not this machine's to rewrite: the berth is a
	// local choice, and putting it in a shared file leaks it into everyone
	// else's checkout.
	//
	// Checked only once a change is actually required. Warning about a tracked
	// file whose port already matches its berth would report a problem that does
	// not exist.
	if !force && IsTracked(path) {
		return nil, &ErrTracked{Service: service, File: path}
	}

	change := &Change{Service: service, File: path, Key: key, From: before, To: want}
	if dryRun {
		return change, nil
	}

	// Back up before the first write, never after: a backup taken afterwards
	// would preserve wharf's own edit rather than the user's original.
	if bk != nil {
		if err := bk.Backup(service, path); err != nil {
			return nil, err
		}
	}
	if err := writeAtomic(path, out); err != nil {
		return nil, err
	}
	return change, nil
}

// currentValue reads the existing value at key, used only for reporting.
func currentValue(raw []byte, format Format, key string) (string, bool) {
	// Re-using SetValue with the sentinel below would mutate; instead rely on
	// the fact that setting a value to itself reports changed=false. Simpler:
	// parse just enough to read it back.
	switch format {
	case FormatDotenv:
		return dotenvValue(raw, key)
	default:
		return yamlValue(raw, key)
	}
}

// writeAtomic replaces a file in one step, so an interrupted write cannot leave
// a developer with a truncated config and a service that will not boot.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".wharf.tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
