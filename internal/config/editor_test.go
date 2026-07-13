package config

import (
	"strings"
	"testing"
)

// The whole point of the surgical editor is that everything except the value is
// byte-identical afterwards. These tests pin that property, because the
// alternative — a decode/re-encode round trip — silently destroys the comments
// in a developer's local config.
func TestSetValueYAMLPreservesEverythingElse(t *testing.T) {
	const src = `# Example service — local config
app:
  name: "example-api"   # display name
  host: "0.0.0.0"
  port: 8085            # collides with another service
  debug: false

database:
  connections:
    postgres:
      host: localhost
      port: 5432        # NOT the listen port
`

	out, changed, err := SetValue([]byte(src), FormatYAML, "app.port", "8103")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	got := string(out)
	if !strings.Contains(got, "port: 8103            # collides with another service") {
		t.Errorf("port not replaced in place, or trailing comment lost:\n%s", got)
	}
	// The database port must be untouched — it is a different key that happens
	// to share the leaf name.
	if !strings.Contains(got, "port: 5432        # NOT the listen port") {
		t.Errorf("database port was disturbed:\n%s", got)
	}
	for _, must := range []string{
		"# Example service — local config",
		`name: "example-api"   # display name`,
		`host: "0.0.0.0"`,
		"debug: false",
	} {
		if !strings.Contains(got, must) {
			t.Errorf("lost %q from the file:\n%s", must, got)
		}
	}
	if strings.Contains(got, "8085") {
		t.Errorf("old port still present:\n%s", got)
	}
}

// A quoted value must stay quoted: some configs type the port as a string and
// unquoting it would change how the app parses it.
func TestSetValuePreservesQuotingStyle(t *testing.T) {
	const src = "db:\n  port: \"9000\"\n"

	out, changed, err := SetValue([]byte(src), FormatYAML, "db.port", "9001")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !strings.Contains(string(out), `port: "9001"`) {
		t.Errorf("quoting style not preserved: %q", string(out))
	}
}

// Writing the value that is already there must be a no-op, so `wharf up` does
// not dirty a file on every launch.
func TestSetValueIdempotent(t *testing.T) {
	const src = "app:\n  port: 8103\n"

	out, changed, err := SetValue([]byte(src), FormatYAML, "app.port", "8103")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if changed {
		t.Error("expected changed=false when the value already matches")
	}
	if string(out) != src {
		t.Errorf("file was rewritten despite no change:\n%q", string(out))
	}
}

// A missing key is an error rather than a silent append: inventing `app.port`
// in a config whose schema has no such field would produce a service that still
// listens on the wrong port, with no indication why.
func TestSetValueMissingKeyErrors(t *testing.T) {
	const src = "app:\n  name: x\n"

	if _, _, err := SetValue([]byte(src), FormatYAML, "app.port", "8103"); err == nil {
		t.Fatal("expected an error for a key that does not exist")
	}
}

func TestSetValueDotenvPreservesComments(t *testing.T) {
	const src = `# local overrides
VITE_API_URL=http://localhost:8080

PORT=3000
DEBUG=true
`

	out, changed, err := SetValue([]byte(src), FormatDotenv, "PORT", "8100")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}

	got := string(out)
	if !strings.Contains(got, "PORT=8100") {
		t.Errorf("PORT not updated:\n%s", got)
	}
	for _, must := range []string{"# local overrides", "VITE_API_URL=http://localhost:8080", "DEBUG=true"} {
		if !strings.Contains(got, must) {
			t.Errorf("lost %q:\n%s", must, got)
		}
	}
}

// Unlike YAML, an absent dotenv key is appended: a .env is routinely partial and
// adding the variable is the intended result.
func TestSetValueDotenvAppendsMissingKey(t *testing.T) {
	const src = "DEBUG=true\n"

	out, changed, err := SetValue([]byte(src), FormatDotenv, "PORT", "8100")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := string(out)
	if !strings.Contains(got, "DEBUG=true") || !strings.Contains(got, "PORT=8100") {
		t.Errorf("append went wrong:\n%q", got)
	}
}

// Regression: a config field holding an address (":8090") must receive an
// address back (":8101"), not a bare integer. Writing `addr: 8101` produces a
// value the program cannot listen on, so the service dies at startup — and it
// would look like wharf "assigned a port" successfully.
func TestRenderPortPreservesAddressShape(t *testing.T) {
	cases := []struct {
		name     string
		template string
		port     int
		want     string
	}{
		{"bare port", "{port}", 8103, "8103"},
		{"colon address", ":{port}", 8101, ":8101"},
		{"host and port", "0.0.0.0:{port}", 8105, "0.0.0.0:8105"},
		{"loopback", "127.0.0.1:{port}", 9091, "127.0.0.1:9091"},
		{"no template falls back to bare", "", 8080, "8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderPort(tc.template, tc.port); got != tc.want {
				t.Errorf("RenderPort(%q, %d) = %q, want %q", tc.template, tc.port, got, tc.want)
			}
		})
	}
}

// JSON is parsed by the same node editor; formatting must survive.
func TestSetValueJSON(t *testing.T) {
	const src = `{
  "server": {
    "port": 9090,
    "host": "127.0.0.1"
  }
}`

	out, changed, err := SetValue([]byte(src), FormatJSON, "server.port", "9091")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	got := string(out)
	if !strings.Contains(got, `"port": 9091`) {
		t.Errorf("port not replaced:\n%s", got)
	}
	if !strings.Contains(got, `"host": "127.0.0.1"`) {
		t.Errorf("sibling key disturbed:\n%s", got)
	}
}
