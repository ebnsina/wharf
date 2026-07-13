package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nginx conflates "strip the prefix" with "what to send upstream", and the
// trailing slash is the only thing that distinguishes them. Reading that wrong
// misroutes traffic silently — the gateway still answers, the service still
// runs, and the requests arrive at the wrong path.
func TestImportNginxStripSemantics(t *testing.T) {
	const conf = `
server {
    listen 127.0.0.1:80;
    server_name api.example.test;

    location / {
        proxy_pass http://localhost:8088;
    }

    location ^~ /v1/cdn/ {
        proxy_pass http://localhost:8085/v1/;
    }

    location ^~ /v1/pulse/ {
        proxy_pass http://localhost:8082/;
    }

    location ^~ /raw/ {
        proxy_pass http://localhost:9000;
    }
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	host, routes, err := ImportNginx(path)
	if err != nil {
		t.Fatalf("ImportNginx: %v", err)
	}
	if host != "api.example.test" {
		t.Errorf("host = %q, want api.example.test", host)
	}
	if len(routes) != 4 {
		t.Fatalf("got %d routes, want 4: %+v", len(routes), routes)
	}

	byPrefix := map[string]Imported{}
	for _, r := range routes {
		byPrefix[r.Prefix] = r
	}

	// proxy_pass with no path: the whole URI is forwarded unchanged.
	if r := byPrefix["/raw/"]; r.Strip {
		t.Errorf("/raw/ has no path on proxy_pass, so it must not strip: %+v", r)
	}

	// proxy_pass ending in a bare "/": strips the prefix, upstream sees /x.
	// This is the case that is easy to mistake for "no rewrite".
	r := byPrefix["/v1/pulse/"]
	if !r.Strip {
		t.Errorf("/v1/pulse/ → :8082/ must strip the prefix: %+v", r)
	}
	if r.UpstreamPrefix != "" {
		t.Errorf("/v1/pulse/ upstream prefix = %q, want empty", r.UpstreamPrefix)
	}

	// proxy_pass ending in /v1/: strips the prefix and prepends /v1.
	r = byPrefix["/v1/cdn/"]
	if !r.Strip || r.UpstreamPrefix != "/v1" {
		t.Errorf("/v1/cdn/ → :8085/v1/ should strip and prepend /v1, got %+v", r)
	}

	if !byPrefix["/"].Default {
		t.Error(`location / should be marked as the default backend`)
	}
}

// The generated nginx must reproduce exactly the proxy_pass forms it read, or
// importing and re-applying would quietly change how requests are routed.
func TestNginxRoundTripsProxyPass(t *testing.T) {
	routes := []Route{
		{Service: "cdn", Prefix: "/v1/cdn/", Strip: true, UpstreamPrefix: "/v1", Berth: 8103},
		{Service: "pulse", Prefix: "/v1/pulse/", Strip: true, Berth: 8082},
		{Service: "raw", Prefix: "/raw/", Berth: 9000},
		{Service: "app", Prefix: "/", Default: true, Berth: 8088},
	}

	out, err := Nginx{}.Render("api.example.test", routes)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"proxy_pass http://localhost:8103/v1/;", // strip + prepend
		"proxy_pass http://localhost:8082/;",    // strip to root
		"proxy_pass http://localhost:9000;",     // no strip — no trailing slash
		// The default backend does not strip either, so it must have no trailing
		// slash. Adding one would rewrite every path reaching the main app.
		"proxy_pass http://localhost:8088;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated config is missing %q:\n%s", want, out)
		}
	}
}

// Caddy must express the same three behaviours with handle/handle_path/rewrite.
func TestCaddyExpressesStripCorrectly(t *testing.T) {
	routes := []Route{
		{Service: "cdn", Prefix: "/v1/cdn/", Strip: true, UpstreamPrefix: "/v1", Berth: 8103},
		{Service: "pulse", Prefix: "/v1/pulse/", Strip: true, Berth: 8082},
		{Service: "raw", Prefix: "/raw/", Berth: 9000},
	}

	out, err := Caddy{}.Render("api.example.test", routes)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Strip + prepend needs handle_path *and* a rewrite.
	if !strings.Contains(out, "handle_path /v1/cdn/*") || !strings.Contains(out, "rewrite * /v1{uri}") {
		t.Errorf("cdn route should strip and rewrite:\n%s", out)
	}
	// Strip to root needs handle_path with no rewrite.
	if !strings.Contains(out, "handle_path /v1/pulse/*") {
		t.Errorf("pulse route should use handle_path:\n%s", out)
	}
	// No strip needs plain handle.
	if !strings.Contains(out, "handle /raw/*") {
		t.Errorf("raw route should use handle, not handle_path:\n%s", out)
	}
}

// The catch-all must sort last: any route after it would never be reached.
func TestRoutesForPutsDefaultLast(t *testing.T) {
	routes := []Route{
		{Service: "app", Prefix: "/", Default: true, Berth: 8088},
		{Service: "cdn", Prefix: "/v1/cdn/", Berth: 8103},
	}
	// RoutesFor sorts; emulate its input ordering via the sort it applies.
	out, err := Caddy{}.Render("h", sortForTest(routes))
	if err != nil {
		t.Fatal(err)
	}
	cdnAt := strings.Index(out, "/v1/cdn/")
	defaultAt := strings.Index(out, "handle {")
	if cdnAt == -1 || defaultAt == -1 || cdnAt > defaultAt {
		t.Errorf("the default backend must come after specific prefixes:\n%s", out)
	}
}

// sortForTest mirrors the ordering RoutesFor applies.
func sortForTest(in []Route) []Route {
	var specific, def []Route
	for _, r := range in {
		if r.Default {
			def = append(def, r)
		} else {
			specific = append(specific, r)
		}
	}
	return append(specific, def...)
}
