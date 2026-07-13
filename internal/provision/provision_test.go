package provision

import (
	"testing"

	"github.com/ebnsina/wharf/internal/manifest"
)

func svc(name string, needs ...manifest.Need) manifest.Service {
	return manifest.Service{Name: name, Kind: manifest.KindService, Needs: needs}
}

func need(typ, dsn string) manifest.Need {
	return manifest.Need{Type: typ, DSN: dsn}
}

// One Postgres holds a database for each of several services. Planning a server
// per service would start six Postgres containers to hold six databases — and
// none of them on the port the configs actually name.
func TestPlanGroupsServicesOntoOneServer(t *testing.T) {
	services := []manifest.Service{
		svc("billing", need("postgres", "postgres://postgres@localhost:5432/billing")),
		svc("auth", need("postgres", "postgres://postgres@localhost:5432/auth")),
		svc("search", need("postgres", "postgres://postgres@localhost:5432/search")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1 holding three databases", len(servers))
	}
	if got := len(servers[0].Databases); got != 3 {
		t.Fatalf("server holds %d databases, want 3", got)
	}
	for _, db := range []string{"billing", "auth", "search"} {
		if _, ok := servers[0].Databases[db]; !ok {
			t.Errorf("database %q was not planned", db)
		}
	}
}

// 127.0.0.1 and localhost are the same machine. Treating them as two servers
// would try to provision the same datastore twice on the same port — the second
// attempt failing on a port collision it caused itself.
func TestPlanTreatsLoopbackAndLocalhostAsOneServer(t *testing.T) {
	services := []manifest.Service{
		svc("a", need("postgres", "postgres://postgres@localhost:5432/a")),
		svc("b", need("postgres", "postgres://postgres@127.0.0.1:5432/b")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1 — 127.0.0.1 and localhost are the same host", len(servers))
	}
}

// Different ports are different servers, even for the same datastore.
func TestPlanSeparatesServersByPort(t *testing.T) {
	services := []manifest.Service{
		svc("a", need("postgres", "postgres://postgres@localhost:5432/a")),
		svc("b", need("postgres", "postgres://cdn:cdn@localhost:5434/b")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want 2 — different ports are different servers", len(servers))
	}
}

// The regression that broke `wharf infra up <service>`: services disagree about
// the password for a shared server, so planning from a subset can drop the only
// credentials that work — and wharf then reports the server as down when it is
// merely refusing an empty password.
func TestPlanForKeepsCredentialsFromTheWholeWorkspace(t *testing.T) {
	all := []manifest.Service{
		// This one declares no password.
		svc("cdn", need("postgres", "postgres://postgres@localhost:5432/cdn")),
		// This one does — and it is the one that actually works.
		svc("kormi", need("postgres", "postgres://postgres:secret@localhost:5432/kormi")),
	}
	selected := []manifest.Service{all[0]} // only cdn

	servers, err := PlanFor(all, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}

	s := servers[0]
	if s.Password != "secret" {
		t.Errorf("password = %q, want \"secret\" — credentials must come from every service", s.Password)
	}
	// ...but only the selected service's database should be provisioned.
	if len(s.Databases) != 1 {
		t.Fatalf("planned %d databases, want only the selected service's", len(s.Databases))
	}
	if _, ok := s.Databases["cdn"]; !ok {
		t.Error("the selected service's database was not planned")
	}
	if _, ok := s.Databases["kormi"]; ok {
		t.Error("a database belonging to an unselected service was planned")
	}
}

// Redis numbers its databases and they always exist, so there is nothing to
// create. Treating "/0" as a database name would have wharf try to create one.
func TestPlanIgnoresRedisDatabaseIndex(t *testing.T) {
	services := []manifest.Service{
		svc("cache", need("redis", "redis://localhost:6379/0")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers[0].Databases) != 0 {
		t.Errorf("redis planned %v as databases; its indexes always exist",
			servers[0].SortedDatabases())
	}
}

// A DSN with no port must still resolve to the datastore's default, or wharf
// would try to reach it on port 0.
func TestPlanFillsInDefaultPort(t *testing.T) {
	services := []manifest.Service{
		svc("a", need("postgres", "postgres://postgres@localhost/a")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Port != 5432 {
		t.Errorf("port = %d, want the Postgres default of 5432", servers[0].Port)
	}
}

// Names come from config files. "stream-v2" is not a valid bare SQL identifier,
// and interpolating it unquoted is a syntax error at CREATE time.
func TestQuoteIdentHandlesHyphens(t *testing.T) {
	if got := quoteIdent("stream-v2"); got != `"stream-v2"` {
		t.Errorf("quoteIdent = %s, want a quoted identifier", got)
	}
	if got := quoteIdent(`we"ird`); got != `"we""ird"` {
		t.Errorf("quoteIdent = %s, want the embedded quote doubled", got)
	}
}

// Two services wanting the same server with different users is a real conflict:
// whichever wharf picks, the other fails to authenticate. It must be reported,
// not silently resolved.
func TestPlanReportsCredentialConflicts(t *testing.T) {
	services := []manifest.Service{
		svc("a", need("postgres", "postgres://alice@localhost:5432/a")),
		svc("b", need("postgres", "postgres://bob@localhost:5432/b")),
	}

	servers, err := Plan(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers[0].Conflicts) == 0 {
		t.Error("two different users for one server should be reported as a conflict")
	}
}
