package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func writeGo(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, "internal", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The bug that made wharf report an endpoint that 404s: the literal in the
// source is not the path the server serves, because the route is registered on
// a group that carries a prefix.
func TestHealthPathResolvesRouterGroups(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "routes.go", `
package routes

func (api *API) register() {
	v1 := api.router.Group("/v1")
	v1.GET("/health-check", api.welcomeCtrl.HealthCheck)
	v1.GET("/users", api.userCtrl.List)
}
`)

	if got := HealthPath(dir); got != "/v1/health-check" {
		t.Errorf("HealthPath = %q, want /v1/health-check — the group prefix must be applied", got)
	}
}

// Groups nest, and a child cannot be resolved before its parent.
func TestHealthPathResolvesNestedGroups(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "routes.go", `
package routes

func register(r *gin.Engine) {
	api := r.Group("/api")
	v2 := api.Group("/v2")
	v2.GET("/healthz", handler)
}
`)

	if got := HealthPath(dir); got != "/api/v2/healthz" {
		t.Errorf("HealthPath = %q, want /api/v2/healthz", got)
	}
}

// A route on the root router has no prefix to add.
func TestHealthPathOnTheRootRouter(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "server.go", `
package api

func setup(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", healthHandler)
}
`)

	if got := HealthPath(dir); got != "/healthz" {
		t.Errorf("HealthPath = %q, want /healthz", got)
	}
}

// A hand-rolled router compares the path instead of registering it.
func TestHealthPathFindsAHandRolledRouter(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "server.go", `
package api

func (s *APIServer) route(method, path string) handler {
	if method == "GET" && path == "/health" {
		return s.handleHealth
	}
	return nil
}
`)

	if got := HealthPath(dir); got != "/health" {
		t.Errorf("HealthPath = %q, want /health", got)
	}
}

// The leaf-name filter is what keeps features out. /network-health contains
// "health" but is an API resource, not a liveness probe — and probing it would
// exercise real work on every check.
func TestHealthPathIgnoresFeatureRoutesThatMerelyContainHealth(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "routes.go", `
package routes

func register(r *gin.Engine) {
	rum := r.Group("/rum")
	rum.GET("/network-health", h.NetworkHealth)
	rum.GET("/test-custom-status", h.Status)
	rum.GET("/ban/status", h.BanStatus)
}
`)

	if got := HealthPath(dir); got != "" {
		t.Errorf("HealthPath = %q, want none — these are features, not liveness endpoints", got)
	}
}

// A service that reaches out to *another* service's health endpoint has not
// declared one of its own. Matching that outbound URL would give wharf a path
// this service does not serve.
func TestHealthPathIgnoresOutboundHealthCalls(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "nodes.go", `
package nodes

func (s *Service) checkNode(ctx context.Context, adminURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/health", nil)
	return err
}
`)

	if got := HealthPath(dir); got != "" {
		t.Errorf("HealthPath = %q, want none — that is a call out, not a route in", got)
	}
}

// Given several, the most specific liveness path wins.
func TestHealthPathPrefersTheDedicatedLivenessPath(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, dir, "routes.go", `
package routes

func register(r *gin.Engine) {
	r.GET("/ping", h.Ping)
	r.GET("/healthz", h.Healthz)
}
`)

	if got := HealthPath(dir); got != "/healthz" {
		t.Errorf("HealthPath = %q, want /healthz — it beats /ping", got)
	}
}
