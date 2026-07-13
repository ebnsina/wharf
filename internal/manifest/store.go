package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNoWorkspace signals that wharf has not been initialised on this machine.
var ErrNoWorkspace = errors.New("no workspace found; run `wharf scan <dir>` first")

// Store persists the workspace and service manifests under a root directory
// (~/.wharf by default). It is the only thing in wharf that touches that
// directory, so relocating state is a one-line change for callers.
type Store struct {
	root string
}

// NewStore returns a Store rooted at dir. If dir is empty it defaults to
// ~/.wharf, honouring WHARF_HOME when set.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		if env := os.Getenv("WHARF_HOME"); env != "" {
			dir = env
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("locate home directory: %w", err)
			}
			dir = filepath.Join(home, ".wharf")
		}
	}
	return &Store{root: dir}, nil
}

// Root is the directory holding all wharf state.
func (s *Store) Root() string { return s.root }

// ServicesDir holds one YAML manifest per service.
func (s *Store) ServicesDir() string { return filepath.Join(s.root, "services") }

// RunDir holds generated config overlays. Never inside a user's repo.
func (s *Store) RunDir(service string) string { return filepath.Join(s.root, "run", service) }

// LogPath is where a process's output is persisted.
func (s *Store) LogPath(service, process string) string {
	return filepath.Join(s.root, "logs", service, process+".log")
}

// StatePath records which services wharf believes are running.
func (s *Store) StatePath() string { return filepath.Join(s.root, "state.json") }

func (s *Store) workspacePath() string { return filepath.Join(s.root, "workspace.yaml") }

// DefaultWorkspace is used when initialising a fresh install. The berth range
// sits above the common defaults (3000/5173/8080) so allocation rarely fights
// with a hard-coded port in some tool wharf does not manage.
func DefaultWorkspace() Workspace {
	return Workspace{
		Roots:      []string{},
		BerthRange: BerthRange{From: 8100, To: 8399},
	}
}

// LoadWorkspace reads workspace.yaml.
func (s *Store) LoadWorkspace() (Workspace, error) {
	var ws Workspace
	data, err := os.ReadFile(s.workspacePath())
	if err != nil {
		if os.IsNotExist(err) {
			return ws, ErrNoWorkspace
		}
		return ws, fmt.Errorf("read workspace: %w", err)
	}
	if err := yaml.Unmarshal(data, &ws); err != nil {
		return ws, fmt.Errorf("parse %s: %w", s.workspacePath(), err)
	}
	if ws.BerthRange.From == 0 || ws.BerthRange.To == 0 {
		def := DefaultWorkspace()
		ws.BerthRange = def.BerthRange
	}
	return ws, nil
}

// SaveWorkspace writes workspace.yaml, creating the root if needed.
func (s *Store) SaveWorkspace(ws Workspace) error {
	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", s.root, err)
	}
	return writeYAML(s.workspacePath(), ws)
}

// LoadServices reads every service manifest, sorted by name for stable output.
func (s *Store) LoadServices() ([]Service, error) {
	dir := s.ServicesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}

	var services []Service
	for _, e := range entries {
		if e.IsDir() || !isYAML(e.Name()) {
			continue
		}
		svc, err := s.loadServiceFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		services = append(services, svc)
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services, nil
}

// LoadService reads a single service manifest by name.
func (s *Store) LoadService(name string) (Service, error) {
	return s.loadServiceFile(s.servicePath(name))
}

func (s *Store) loadServiceFile(path string) (Service, error) {
	var svc Service
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return svc, fmt.Errorf("unknown service %q", nameFromPath(path))
		}
		return svc, fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, &svc); err != nil {
		return svc, fmt.Errorf("parse %s: %w", path, err)
	}
	if svc.Name == "" {
		svc.Name = nameFromPath(path)
	}
	return svc, nil
}

// SaveService writes one service manifest.
func (s *Store) SaveService(svc Service) error {
	if svc.Name == "" {
		return errors.New("service has no name")
	}
	if err := os.MkdirAll(s.ServicesDir(), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", s.ServicesDir(), err)
	}
	return writeYAML(s.servicePath(svc.Name), svc)
}

func (s *Store) servicePath(name string) string {
	return filepath.Join(s.ServicesDir(), name+".yaml")
}

// writeYAML marshals v and writes it atomically, so an interrupted write cannot
// leave a half-parsed manifest behind.
func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func nameFromPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
