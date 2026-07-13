package cli

import (
	"fmt"
	"strings"

	"github.com/ebnsina/wharf/internal/gateway"
	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Generate the local API gateway config from the manifests",
	}
	cmd.AddCommand(newGatewayImportCmd(), newGatewayApplyCmd())
	return cmd
}

func newGatewayImportCmd() *cobra.Command {
	var driver string

	c := &cobra.Command{
		Use:   "import <nginx.conf>",
		Short: "Adopt the routes from an existing nginx config",
		Long: "Reads the routes you already have and attaches each one to the service that\n" +
			"owns its port, so you do not retype them. From then on the route follows the\n" +
			"berth: change a port and the gateway config regenerates to match.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGatewayImport(args[0], driver)
		},
	}
	c.Flags().StringVar(&driver, "driver", "nginx", "gateway to generate from now on (caddy|nginx)")
	return c
}

func runGatewayImport(path, driver string) error {
	st, err := store()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}
	ws, err := st.LoadWorkspace()
	if err != nil {
		return err
	}

	host, routes, err := gateway.ImportNginx(path)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return fmt.Errorf("no routes found in %s", path)
	}

	// Match each route to the service that *declared* the port the route names,
	// not to whoever currently holds it.
	//
	// An existing gateway was written against the original ports. wharf has
	// since moved services off contested ones, so the current occupant of :8085
	// is whichever service won that collision — not the one the route was
	// written for. Matching on the current berth would silently reroute traffic
	// to the wrong service, which is far worse than failing to import.
	byDeclared := map[int][]*manifest.Service{}
	for i := range services {
		if d := services[i].Declared; d > 0 {
			byDeclared[d] = append(byDeclared[d], &services[i])
		}
	}

	matched, unresolved := 0, 0
	for _, r := range routes {
		candidates := byDeclared[r.Port]

		switch len(candidates) {
		case 0:
			ui.Warn("%-16s :%d — no service ever declared that port; dead upstream", r.Prefix, r.Port)
			unresolved++
			continue

		case 1:
			// Unambiguous.

		default:
			// Several services shipped with the same port, so the port alone
			// cannot say which one the route meant. The prefix usually can:
			// /v1/cdn/ belongs to the service with "cdn" in its name.
			pick, ok := byName(candidates, r.Prefix)
			if !ok {
				var names []string
				for _, c := range candidates {
					names = append(names, c.Name)
				}
				ui.Fail("%-16s :%d is ambiguous — declared by %s",
					r.Prefix, r.Port, strings.Join(names, ", "))
				ui.Note("    set `route:` by hand in the manifest of whichever one owns this prefix")
				unresolved++
				continue
			}
			// Inferred, not proven. Say so, because a wrong guess here routes
			// traffic to the wrong service and looks like a bug in the service.
			ui.Warn("%-16s :%d was declared by %d services; matched %s on its name",
				r.Prefix, r.Port, len(candidates), ui.Bold.Render(pick.Name))
			candidates = []*manifest.Service{pick}
		}

		svc := candidates[0]
		svc.Route = &manifest.Route{
			Prefix:         r.Prefix,
			Strip:          r.Strip,
			UpstreamPrefix: r.UpstreamPrefix,
			Default:        r.Default,
		}
		if err := st.SaveService(*svc); err != nil {
			return err
		}

		moved := ""
		if svc.Berth != r.Port {
			moved = ui.Dim.Render(fmt.Sprintf("  (was :%d, now on its berth)", r.Port))
		}
		ui.Ok("%-16s → %s :%d%s", r.Prefix, svc.Name, svc.Berth, moved)
		matched++
	}

	ws.Gateway = manifest.GatewayConfig{
		Driver:     driver,
		Host:       host,
		ConfigPath: path,
	}
	if err := st.SaveWorkspace(ws); err != nil {
		return err
	}

	fmt.Println()
	ui.Ok("imported %d routes for %s", matched, host)
	if unresolved > 0 {
		ui.Warn("%d routes could not be resolved and were not imported", unresolved)
	}
	ui.Note("`wharf gateway apply --dry-run` to see the generated config")
	return nil
}

// byName breaks a port tie using the route's own prefix: "/v1/cdn/" names the
// service with "cdn" in it. It only decides when exactly one candidate matches —
// two matches are no more resolved than none.
func byName(candidates []*manifest.Service, prefix string) (*manifest.Service, bool) {
	// "/v1/livestream/" -> "livestream": the last non-version path segment.
	var keyword string
	for _, seg := range strings.Split(strings.Trim(prefix, "/"), "/") {
		if seg == "" || isVersionSegment(seg) {
			continue
		}
		keyword = strings.ToLower(seg)
	}
	if keyword == "" {
		return nil, false
	}

	var hits []*manifest.Service
	for _, c := range candidates {
		if strings.Contains(strings.ToLower(c.Name), keyword) {
			hits = append(hits, c)
		}
	}
	if len(hits) != 1 {
		return nil, false
	}
	return hits[0], true
}

// isVersionSegment matches "v1", "v2" and friends, which carry no service name.
func isVersionSegment(seg string) bool {
	if len(seg) < 2 || (seg[0] != 'v' && seg[0] != 'V') {
		return false
	}
	for _, r := range seg[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func newGatewayApplyCmd() *cobra.Command {
	var dryRun bool

	c := &cobra.Command{
		Use:   "apply",
		Short: "Write the gateway config and reload the proxy",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGatewayApply(dryRun)
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the config instead of writing it")
	return c
}

func runGatewayApply(dryRun bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	ws, err := st.LoadWorkspace()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}

	if ws.Gateway.Driver == "" {
		return fmt.Errorf("no gateway configured; run `wharf gateway import <nginx.conf>` first")
	}

	body, err := gateway.Apply(ws.Gateway, services, dryRun)
	if err != nil {
		return err
	}

	if dryRun {
		fmt.Println(body)
		ui.Note("dry run — nothing written, nothing reloaded")
		return nil
	}

	ui.Ok("wrote %s and reloaded %s", ws.Gateway.ConfigPath, ws.Gateway.Driver)
	return nil
}
