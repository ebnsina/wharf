package gateway

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Imported is a route read out of an existing nginx config, matched back to the
// service that owns the port.
type Imported struct {
	Prefix         string
	Strip          bool
	UpstreamPrefix string
	Port           int
	Default        bool
}

var (
	serverNameRe = regexp.MustCompile(`(?m)^\s*server_name\s+([^;]+);`)
	locationRe   = regexp.MustCompile(`(?m)^\s*location\s+(?:\^~\s+)?(\S+)\s*\{`)
	proxyPassRe  = regexp.MustCompile(`(?m)^\s*proxy_pass\s+http://[^:]+:(\d+)(/[^;\s]*)?;`)
)

// ImportNginx reads an existing nginx config and recovers its routes.
//
// The alternative is asking someone to retype five routes they already have,
// which is both tedious and a chance to introduce a typo into the one file whose
// whole purpose is to be correct.
func ImportNginx(path string) (host string, routes []Imported, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", path, err)
	}
	body := string(raw)

	if m := serverNameRe.FindStringSubmatch(body); m != nil {
		host = strings.TrimSpace(m[1])
	}

	// Walk locations in order, pairing each with the next proxy_pass that
	// follows it. Nginx blocks are not nested here, so position is enough and a
	// full parser would be more machinery than the problem deserves.
	locs := locationRe.FindAllStringSubmatchIndex(body, -1)
	for _, loc := range locs {
		prefix := body[loc[2]:loc[3]]

		rest := body[loc[1]:]
		pass := proxyPassRe.FindStringSubmatch(rest)
		if pass == nil {
			continue
		}
		port, err := strconv.Atoi(pass[1])
		if err != nil {
			continue
		}

		imp := Imported{
			Prefix:  prefix,
			Port:    port,
			Default: prefix == "/",
		}

		// In nginx it is the *presence of a path* on proxy_pass that strips the
		// matched location prefix — and a bare "/" is a path. So:
		//
		//   proxy_pass http://host:8082      → no strip, whole URI forwarded
		//   proxy_pass http://host:8082/     → strip, upstream sees /x
		//   proxy_pass http://host:8082/v1/  → strip, upstream sees /v1/x
		//
		// Treating the "/" case as "no strip" is the subtle mistake: it forwards
		// /v1/pulse/x upstream unchanged instead of as /x.
		if upstream := pass[2]; upstream != "" {
			imp.Strip = true
			imp.UpstreamPrefix = strings.TrimSuffix(upstream, "/")
		}
		routes = append(routes, imp)
	}
	return host, routes, nil
}
