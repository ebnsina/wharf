// Package link finds and repairs the URLs one project uses to reach another.
//
// A frontend reaches its API through a hard-coded localhost URL in a .env or a
// vite config. When wharf moves a service off a contested port, every such URL
// still names the old one — so the frontend quietly calls whatever service now
// answers there, or nothing at all. The app compiles, the dev server starts, and
// the requests go to the wrong place.
//
// This is the same drift the gateway generator exists to prevent, in a different
// file. The fix is the same: derive the port from the service that owns it,
// rather than trusting a number somebody typed once.
package link

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Ref is one URL in one file that points at a local port.
type Ref struct {
	// Service is the project the reference lives in.
	Service string
	File    string
	Line    int
	// Key is the variable or field holding it, for reporting.
	Key string
	// Port is the port the URL names.
	Port int
	// Text is the matched host:port, e.g. "localhost:8090".
	Text string
}

// Resolution is what wharf worked out about a reference.
type Resolution struct {
	Ref
	// Target is the service that owns this port. Empty when nothing does.
	Target string
	// Want is the port the target actually listens on now.
	Want int
	// Ambiguous lists the services that all declared this port, when more than
	// one did and wharf cannot tell which was meant.
	Ambiguous []string
	// Inferred marks a target wharf worked out from the variable's name rather
	// than proved from the port. Reported, never assumed silently: a wrong guess
	// sends a frontend's traffic to the wrong backend, which looks like a bug in
	// the backend.
	Inferred bool
}

// Stale reports whether the reference points somewhere the target no longer is.
func (r Resolution) Stale() bool {
	return r.Target != "" && r.Want != 0 && r.Want != r.Port
}

// Dangling reports a reference to a port no service owns.
func (r Resolution) Dangling() bool {
	return r.Target == "" && len(r.Ambiguous) == 0
}

// scannable are the files that hold inter-service URLs. Source files are not
// scanned: a URL in code is a decision the developer made deliberately, and
// rewriting source is not wharf's business.
var scannable = []string{
	".env", ".env.local", ".env.development", ".env.development.local",
	"vite.config.ts", "vite.config.js",
	"next.config.js", "next.config.mjs", "next.config.ts",
}

// localURL matches a loopback host and port inside any text.
var localURL = regexp.MustCompile(`(?:localhost|127\.0\.0\.1):(\d{2,5})`)

// keyOf pulls the variable name off a dotenv line, for reporting.
var keyLine = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// Scan finds every local URL reference in a service's config files.
func Scan(svc manifest.Service) []Ref {
	var refs []Ref

	for _, name := range scannable {
		path := filepath.Join(svc.Path, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		refs = append(refs, scanText(svc.Name, name, string(data))...)
	}

	// A Go service can carry its own SPA under web/, with its own config that
	// proxies back to the Go server. That file drifts exactly like any other.
	for _, name := range scannable {
		path := filepath.Join(svc.Path, "web", name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		refs = append(refs, scanText(svc.Name, filepath.Join("web", name), string(data))...)
	}
	return refs
}

func scanText(service, file, body string) []Ref {
	var refs []Ref

	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "//") {
			continue
		}

		for _, m := range localURL.FindAllStringSubmatch(line, -1) {
			port, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			key := ""
			if k := keyLine.FindStringSubmatch(line); k != nil {
				key = k[1]
			}
			refs = append(refs, Ref{
				Service: service,
				File:    file,
				Line:    i + 1,
				Key:     key,
				Port:    port,
				Text:    m[0],
			})
		}
	}
	return refs
}

// Resolve works out which service each reference was meant for.
//
// Matching is on the port a service *declared*, not the one it listens on now:
// the URL was written when the target still used its original port, so that is
// the number it names.
func Resolve(refs []Ref, services []manifest.Service) []Resolution {
	byDeclared := map[int][]manifest.Service{}
	byName := map[string]manifest.Service{}
	links := map[string]map[string]string{}

	for _, svc := range services {
		byName[svc.Name] = svc
		if d := svc.Declared; d > 0 {
			byDeclared[d] = append(byDeclared[d], svc)
		}
		if len(svc.Links) > 0 {
			links[svc.Name] = svc.Links
		}
	}

	out := make([]Resolution, 0, len(refs))
	for _, ref := range refs {
		res := Resolution{Ref: ref}

		// An explicit link in the manifest is the user's own answer, and it
		// outranks anything wharf could infer.
		if target, ok := links[ref.Service][ref.Key]; ok {
			if svc, known := byName[target]; known {
				res.Target, res.Want = svc.Name, svc.Berth
				out = append(out, res)
				continue
			}
			res.Ambiguous = []string{"links names unknown service " + target}
			out = append(out, res)
			continue
		}

		owners := byDeclared[ref.Port]

		switch len(owners) {
		case 0:
			// Nothing declared this port. It may be infrastructure (5432, 6379)
			// or something wharf does not manage; either way it is not stale.
		case 1:
			res.Target, res.Want = owners[0].Name, owners[0].Berth
		default:
			// Several services shipped with this port, so the port alone cannot
			// say which was meant.

			// A reference inside one of them almost certainly means itself — a
			// Vite config proxying to the Go server in the same repo.
			if self := selfOwner(owners, ref.Service); self != nil {
				res.Target, res.Want = self.Name, self.Berth
				break
			}

			// Otherwise the variable name usually says: VITE_LIVESTREAM_API_URL
			// means the livestream service, whatever port it happens to use.
			if pick, ok := byKeyword(owners, ref.Key); ok {
				res.Target, res.Want = pick.Name, pick.Berth
				res.Inferred = true
				break
			}

			for _, o := range owners {
				res.Ambiguous = append(res.Ambiguous, o.Name)
			}
		}
		out = append(out, res)
	}
	return out
}

// byKeyword picks the candidate whose name the variable names.
//
// VITE_LIVESTREAM_API_URL and vidinfra-livestream-api share "livestream"; the
// other three services that also declared :8080 do not. It only decides when
// exactly one candidate matches — two matches are no more resolved than none.
func byKeyword(owners []manifest.Service, key string) (manifest.Service, bool) {
	if key == "" {
		return manifest.Service{}, false
	}
	// VITE_LIVESTREAM_API_URL -> [vite livestream api url]
	words := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '_' || r == '-'
	})

	var hits []manifest.Service
	for _, o := range owners {
		name := strings.ToLower(o.Name)
		for _, w := range words {
			// Short words match too loosely: "api" appears in nearly every
			// service name and would match all of them.
			if len(w) < 5 || noise[w] {
				continue
			}
			if strings.Contains(name, w) {
				hits = append(hits, o)
				break
			}
		}
	}
	if len(hits) != 1 {
		return manifest.Service{}, false
	}
	return hits[0], true
}

// noise are the words that appear in almost every variable name and so
// distinguish nothing.
var noise = map[string]bool{
	"public": true, "base": true, "http": true, "https": true,
	"local": true, "localhost": true, "server": true, "backend": true,
	"frontend": true, "service": true,
}

// selfOwner returns the service the reference lives in, if it is among the
// candidates.
func selfOwner(owners []manifest.Service, service string) *manifest.Service {
	for i := range owners {
		if owners[i].Name == service {
			return &owners[i]
		}
	}
	return nil
}

// Apply rewrites the stale references in one file, returning what it changed.
//
// Only the host:port token is touched. The rest of the line — the scheme, the
// path, the quoting, the surrounding TypeScript — is left byte-for-byte alone.
func Apply(svc manifest.Service, resolutions []Resolution, dryRun bool) ([]Resolution, error) {
	byFile := map[string][]Resolution{}
	for _, r := range resolutions {
		if r.Service != svc.Name || !r.Stale() {
			continue
		}
		byFile[r.File] = append(byFile[r.File], r)
	}

	var applied []Resolution

	for file, refs := range byFile {
		path := filepath.Join(svc.Path, file)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		lines := strings.Split(string(data), "\n")
		changed := false

		for _, r := range refs {
			idx := r.Line - 1
			if idx < 0 || idx >= len(lines) {
				continue
			}
			host := strings.Split(r.Text, ":")[0]
			old := r.Text
			replacement := host + ":" + strconv.Itoa(r.Want)

			if !strings.Contains(lines[idx], old) {
				continue
			}
			lines[idx] = strings.ReplaceAll(lines[idx], old, replacement)
			changed = true
			applied = append(applied, r)
		}

		if !changed || dryRun {
			continue
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", path, err)
		}
	}
	return applied, nil
}
