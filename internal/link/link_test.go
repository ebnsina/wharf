package link

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebnsina/wharf/internal/manifest"
)

func svc(name string, declared, berth int) manifest.Service {
	return manifest.Service{
		Name: name, Kind: manifest.KindService,
		Declared: declared, Berth: berth,
	}
}

func writeEnv(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The whole point: a URL naming a port its target has moved off is stale, and
// nothing about it fails loudly — the frontend just calls whatever now answers
// there, or nothing at all.
func TestResolveFindsStaleURL(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "VITE_API_URL=http://localhost:8090/v1\n")

	web := svc("web", 3000, 3000)
	web.Path = dir
	api := svc("api", 8090, 8101) // moved off 8090

	refs := Scan(web)
	if len(refs) != 1 {
		t.Fatalf("found %d references, want 1", len(refs))
	}

	res := Resolve(refs, []manifest.Service{web, api})
	if !res[0].Stale() {
		t.Fatal("a URL pointing at a port its target has left is stale")
	}
	if res[0].Target != "api" || res[0].Want != 8101 {
		t.Fatalf("resolved to (%q,%d), want (api,8101)", res[0].Target, res[0].Want)
	}
}

// Matching is on the port the target *declared*, not the one it listens on now:
// the URL was written back when the target still used its original port.
func TestResolveMatchesOnDeclaredPort(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "VITE_API_URL=http://localhost:8101\n")

	web := svc("web", 3000, 3000)
	web.Path = dir
	api := svc("api", 8090, 8101)

	res := Resolve(Scan(web), []manifest.Service{web, api})
	// 8101 is the *current* berth, which no service declared — so nothing owns
	// this reference and it must not be rewritten.
	if res[0].Stale() {
		t.Error("a URL already pointing at the current berth is not stale")
	}
}

// A generic variable naming a port that four services shipped with is genuinely
// ambiguous. Guessing would send a frontend's traffic to the wrong backend,
// which then looks like a bug in the backend.
func TestResolveRefusesToGuessWhenAmbiguous(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "VITE_API_URL=http://localhost:8080\n")

	web := svc("web", 3000, 3000)
	web.Path = dir

	res := Resolve(Scan(web), []manifest.Service{
		web,
		svc("auth-api", 8080, 8105),
		svc("billing-api", 8080, 8106),
	})

	if res[0].Target != "" {
		t.Errorf("resolved to %q; two services declared :8080 and neither is named", res[0].Target)
	}
	if len(res[0].Ambiguous) != 2 {
		t.Errorf("ambiguous = %v, want both candidates listed", res[0].Ambiguous)
	}
}

// ...but the variable's own name usually says which one.
func TestResolveDisambiguatesOnVariableName(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "VITE_BILLING_API_URL=http://localhost:8080\n")

	web := svc("web", 3000, 3000)
	web.Path = dir

	res := Resolve(Scan(web), []manifest.Service{
		web,
		svc("auth-api", 8080, 8105),
		svc("billing-api", 8080, 8106),
	})

	if res[0].Target != "billing-api" || res[0].Want != 8106 {
		t.Fatalf("resolved to (%q,%d), want billing-api on 8106", res[0].Target, res[0].Want)
	}
	if !res[0].Inferred {
		t.Error("a target worked out from a name should be marked inferred, not presented as proven")
	}
}

// An explicit link in the manifest is the user's own answer and outranks
// anything wharf could infer.
func TestExplicitLinkWins(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "VITE_API_URL=http://localhost:8080\n")

	web := svc("web", 3000, 3000)
	web.Path = dir
	web.Links = map[string]string{"VITE_API_URL": "auth-api"}

	res := Resolve(Scan(web), []manifest.Service{
		web,
		svc("auth-api", 8080, 8105),
		svc("billing-api", 8080, 8106),
	})

	if res[0].Target != "auth-api" || res[0].Want != 8105 {
		t.Fatalf("resolved to (%q,%d), want the explicitly linked auth-api", res[0].Target, res[0].Want)
	}
}

// A reference inside the very service that owns the port means itself — a Vite
// config proxying to the Go server in the same repo.
func TestSelfReferenceResolvesToItself(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, filepath.Join("web", ".env"), "VITE_API_URL=http://localhost:8090\n")

	dash := svc("dashboard", 8090, 8101)
	dash.Path = dir

	res := Resolve(Scan(dash), []manifest.Service{
		dash,
		svc("other-api", 8090, 8110), // also declared 8090
	})

	if res[0].Target != "dashboard" || res[0].Want != 8101 {
		t.Fatalf("resolved to (%q,%d), want the service the file lives in", res[0].Target, res[0].Want)
	}
}

// Only the host:port token changes. The scheme, path, quoting and comments must
// survive byte-for-byte.
func TestApplyRewritesOnlyTheHostPort(t *testing.T) {
	dir := t.TempDir()
	const src = `# the API the dashboard talks to
VITE_API_URL=http://localhost:8090/v1/admin
VITE_TOKEN=secret
`
	writeEnv(t, dir, ".env", src)

	web := svc("web", 3000, 3000)
	web.Path = dir
	api := svc("api", 8090, 8101)

	res := Resolve(Scan(web), []manifest.Service{web, api})
	if _, err := Apply(web, res, false); err != nil {
		t.Fatal(err)
	}

	out, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)

	want := "VITE_API_URL=http://localhost:8101/v1/admin"
	if !contains(got, want) {
		t.Errorf("got:\n%s\nwant a line %q — the path must survive", got, want)
	}
	for _, must := range []string{"# the API the dashboard talks to", "VITE_TOKEN=secret"} {
		if !contains(got, must) {
			t.Errorf("lost %q from the file:\n%s", must, got)
		}
	}
}

// Infrastructure ports belong to nobody and must be left alone: rewriting 5432
// would point a service at a Postgres that is not there.
func TestResolveIgnoresPortsNoServiceOwns(t *testing.T) {
	dir := t.TempDir()
	writeEnv(t, dir, ".env", "DATABASE_URL=postgres://u@localhost:5432/db\n")

	api := svc("api", 8090, 8101)
	api.Path = dir

	res := Resolve(Scan(api), []manifest.Service{api})
	if res[0].Stale() {
		t.Error("5432 belongs to Postgres, not to a wharf service; it must not be rewritten")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && stringsContains(haystack, needle)
}

func stringsContains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
