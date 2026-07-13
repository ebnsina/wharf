package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ebnsina/wharf/internal/manifest"
)

// chdir moves into dir for the duration of a test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestCurrentServiceFindsTheProjectYouAreStandingIn(t *testing.T) {
	root := t.TempDir()
	api := filepath.Join(root, "auth-api")
	if err := os.MkdirAll(filepath.Join(api, "internal", "handler"), 0o755); err != nil {
		t.Fatal(err)
	}

	services := []manifest.Service{
		{Name: "auth-api", Path: api},
		{Name: "billing", Path: filepath.Join(root, "billing")},
	}

	// From the project root.
	chdir(t, api)
	svc, ok := currentService(services)
	if !ok || svc.Name != "auth-api" {
		t.Fatalf("at the project root got (%q,%v), want auth-api", svc.Name, ok)
	}

	// And from deep inside it — you are still in that project.
	chdir(t, filepath.Join(api, "internal", "handler"))
	svc, ok = currentService(services)
	if !ok || svc.Name != "auth-api" {
		t.Fatalf("from a subdirectory got (%q,%v), want auth-api", svc.Name, ok)
	}
}

// The bug a plain string-prefix test would cause: "/work/api" is a prefix of
// "/work/api-v2", so standing in api-v2 would answer with api — and `wharf up`
// would start the wrong service.
func TestCurrentServiceDoesNotMatchOnNamePrefix(t *testing.T) {
	root := t.TempDir()
	api := filepath.Join(root, "api")
	apiV2 := filepath.Join(root, "api-v2")
	for _, d := range []string{api, apiV2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	services := []manifest.Service{
		{Name: "api", Path: api},
		{Name: "api-v2", Path: apiV2},
	}

	chdir(t, apiV2)
	svc, ok := currentService(services)
	if !ok || svc.Name != "api-v2" {
		t.Fatalf("got %q, want api-v2 — a path prefix is not a parent directory", svc.Name)
	}
}

// A monorepo child is more specific than its parent, so it wins.
func TestCurrentServicePrefersTheDeepestMatch(t *testing.T) {
	root := t.TempDir()
	mono := filepath.Join(root, "mono")
	child := filepath.Join(mono, "mono-api")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	services := []manifest.Service{
		{Name: "mono", Path: mono},
		{Name: "mono-api", Path: child},
	}

	chdir(t, child)
	svc, ok := currentService(services)
	if !ok || svc.Name != "mono-api" {
		t.Fatalf("got %q, want mono-api — the deeper path is the more specific answer", svc.Name)
	}
}

func TestCurrentServiceOutsideAnyProject(t *testing.T) {
	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}

	services := []manifest.Service{
		{Name: "auth-api", Path: filepath.Join(root, "auth-api")},
	}

	chdir(t, elsewhere)
	if _, ok := currentService(services); ok {
		t.Error("standing outside every project should resolve to nothing")
	}
}

// An explicit name always beats the directory: `wharf up billing` from inside
// auth-api must start billing.
func TestRequireServicePrefersAnExplicitName(t *testing.T) {
	root := t.TempDir()
	api := filepath.Join(root, "auth-api")
	if err := os.MkdirAll(api, 0o755); err != nil {
		t.Fatal(err)
	}

	services := []manifest.Service{
		{Name: "auth-api", Path: api},
		{Name: "billing", Path: filepath.Join(root, "billing")},
	}

	chdir(t, api)
	svc, err := requireService(services, []string{"billing"})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "billing" {
		t.Errorf("got %q, want billing — an explicit argument outranks the directory", svc.Name)
	}
}

func TestRequireServiceOutsideAProjectAsksForAName(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)

	services := []manifest.Service{
		{Name: "auth-api", Path: filepath.Join(root, "auth-api")},
	}

	if _, err := requireService(services, nil); err == nil {
		t.Error("expected an error telling the user to name a service")
	}
}
