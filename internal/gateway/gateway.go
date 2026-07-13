// Package gateway generates the local reverse-proxy config from the manifests.
//
// The point is to remove a duplicated source of truth. A hand-written gateway
// repeats every service's port, so a port and its route are two facts that must
// be kept in agreement by hand — and they drift. Generating the config from the
// same manifests that assign berths makes drift impossible: the route cannot
// name a port the service is not on, because nobody types the port twice.
package gateway

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ebnsina/wharf/internal/manifest"
)

// Route is one upstream the gateway proxies to.
//
// Stripping and rewriting are two separate facts, because nginx conflates them
// in a way that is easy to get wrong: `proxy_pass http://host:8082/` strips the
// matched prefix (the trailing slash is what does it), while `proxy_pass
// http://host:8082` preserves the whole URI. Collapsing those into one field
// silently sends /v1/pulse/x upstream as /v1/pulse/x instead of /x.
type Route struct {
	Service string
	Prefix  string
	// Strip removes Prefix from the path before proxying.
	Strip bool
	// UpstreamPrefix is prepended after stripping: "/v1" turns /v1/cdn/x into
	// /v1/x. Empty means the stripped path is sent as-is.
	UpstreamPrefix string
	Berth          int
	Default        bool
}

// NginxProxyPath renders the path portion of an nginx proxy_pass directive.
func (r Route) NginxProxyPath() string {
	if !r.Strip {
		return ""
	}
	return r.UpstreamPrefix + "/"
}

// Driver renders and reloads a particular proxy. Adding Traefik means adding a
// Driver, not touching anything that calls one.
type Driver interface {
	// Name identifies the driver.
	Name() string
	// Render produces the config file body.
	Render(host string, routes []Route) (string, error)
	// Reload makes the proxy adopt the written config.
	Reload(configPath string) error
}

// Drivers returns the available drivers by name.
func Drivers() map[string]Driver {
	return map[string]Driver{
		"caddy": Caddy{},
		"nginx": Nginx{},
	}
}

// RoutesFor collects the routes declared by the services, ordered most specific
// first so that `/v1/cdn/` is matched before the catch-all `/`.
func RoutesFor(services []manifest.Service) []Route {
	var routes []Route
	for _, svc := range services {
		if svc.Route == nil || svc.Kind != manifest.KindService || svc.Disabled {
			continue
		}
		if svc.Berth == 0 {
			// A route with no berth would generate a proxy_pass to port 0.
			// Skipping it and letting doctor complain beats emitting a config
			// that nginx will refuse to load.
			continue
		}
		routes = append(routes, Route{
			Service:        svc.Name,
			Prefix:         svc.Route.Prefix,
			Strip:          svc.Route.Strip,
			UpstreamPrefix: svc.Route.UpstreamPrefix,
			Berth:          svc.Berth,
			Default:        svc.Route.Default,
		})
	}

	sort.Slice(routes, func(i, j int) bool {
		// The default backend always sorts last: it matches everything, so any
		// route after it would be dead.
		if routes[i].Default != routes[j].Default {
			return !routes[i].Default
		}
		if len(routes[i].Prefix) != len(routes[j].Prefix) {
			return len(routes[i].Prefix) > len(routes[j].Prefix)
		}
		return routes[i].Prefix < routes[j].Prefix
	})
	return routes
}

// Apply renders the gateway config and reloads the proxy.
func Apply(cfg manifest.GatewayConfig, services []manifest.Service, dryRun bool) (string, error) {
	if cfg.Driver == "" {
		return "", fmt.Errorf("no gateway driver configured")
	}
	driver, ok := Drivers()[cfg.Driver]
	if !ok {
		return "", fmt.Errorf("unknown gateway driver %q", cfg.Driver)
	}

	routes := RoutesFor(services)
	if len(routes) == 0 {
		return "", fmt.Errorf("no services declare a route")
	}

	body, err := driver.Render(cfg.Host, routes)
	if err != nil {
		return "", err
	}
	if dryRun {
		return body, nil
	}

	if cfg.ConfigPath == "" {
		return "", fmt.Errorf("gateway.config_path is not set")
	}
	if err := writeFile(cfg.ConfigPath, body); err != nil {
		return "", err
	}
	if err := driver.Reload(cfg.ConfigPath); err != nil {
		return body, fmt.Errorf("wrote %s but could not reload: %w", cfg.ConfigPath, err)
	}
	return body, nil
}

func writeFile(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// run executes a reload command, surfacing the proxy's own error text — which is
// usually precise about what it disliked.
func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s: %s", err, msg)
	}
	return nil
}
