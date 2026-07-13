package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ebnsina/wharf/internal/config"
	"github.com/ebnsina/wharf/internal/link"

	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newLinkCmd() *cobra.Command {
	var dryRun, force bool

	cmd := &cobra.Command{
		Use:   "link [service...]",
		Short: "Repoint the URLs one project uses to reach another",
		Long: "A frontend reaches its API through a hard-coded localhost URL in a .env or a\n" +
			"vite config. When a service moves off a contested port, every such URL still\n" +
			"names the old one — so the app quietly calls whatever now answers there, or\n" +
			"nothing at all. Nothing fails loudly; the requests just go to the wrong place.\n\n" +
			"wharf rewrites only the host:port token, leaving the rest of the line alone.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLink(args, dryRun, force)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the edits without making them")
	cmd.Flags().BoolVar(&force, "force", false, "rewrite even a git-tracked file")
	cmd.AddCommand(newLinkSetCmd())
	return cmd
}

func newLinkSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <service> <VAR>=<target>...",
		Short: "Record which service a config variable points at",
		Long: "For a variable wharf cannot resolve on its own — a generic VITE_API_URL naming\n" +
			"a port several services shipped with — say once which service it means. wharf\n" +
			"keeps the URL correct from then on, however often that service moves.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLinkSet(args[0], args[1:])
		},
	}
}

func runLinkSet(service string, pairs []string) error {
	st, err := store()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}

	svc, err := requireService(services, []string{service})
	if err != nil {
		return err
	}

	known := map[string]bool{}
	for _, s := range services {
		known[s.Name] = true
	}

	if svc.Links == nil {
		svc.Links = map[string]string{}
	}

	for _, pair := range pairs {
		key, target, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("expected VAR=service, got %q", pair)
		}
		if !known[target] {
			return fmt.Errorf("unknown service %q", target)
		}
		svc.Links[key] = target
		ui.Ok("%s: %s → %s", svc.Name, key, ui.Bold.Render(target))
	}

	if err := st.SaveService(svc); err != nil {
		return err
	}
	ui.Note("run `wharf link` to apply it")
	return nil
}

func runLink(names []string, dryRun, force bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	all, err := st.LoadServices()
	if err != nil {
		return err
	}

	targets := all
	if len(names) > 0 {
		targets = selectServices(all, names)
		if len(targets) == 0 {
			return fmt.Errorf("no matching services")
		}
	}

	var (
		fixed     int
		ambiguous []link.Resolution
		skipped   []string
	)

	for _, svc := range targets {
		refs := link.Scan(svc)
		if len(refs) == 0 {
			continue
		}
		resolved := link.Resolve(refs, all)

		for _, r := range resolved {
			if len(r.Ambiguous) > 0 {
				ambiguous = append(ambiguous, r)
			}
		}

		var stale []link.Resolution
		for _, r := range resolved {
			if r.Stale() {
				stale = append(stale, r)
			}
		}
		if len(stale) == 0 {
			continue
		}

		// A config committed to git is not this machine's to rewrite: the berth
		// is a local choice, and putting it in a shared file lands it in
		// everyone else's checkout.
		var writable []link.Resolution
		for _, r := range stale {
			path := filepath.Join(svc.Path, r.File)
			if !force && config.IsTracked(path) {
				skipped = append(skipped, fmt.Sprintf("%s (%s is committed to git)", svc.Name, r.File))
				continue
			}
			writable = append(writable, r)
		}
		if len(writable) == 0 {
			continue
		}

		applied, err := link.Apply(svc, writable, dryRun)
		if err != nil {
			ui.Fail("%s — %v", svc.Name, err)
			continue
		}

		for _, r := range applied {
			how := r.Target
			if r.Inferred {
				// Say how this one was worked out, not merely that it was: an
				// inference the user cannot check is one they have to trust.
				how = r.Target + " — inferred from " + r.Why
			}
			ui.Ok("%-28s %s → %s  %s",
				svc.Name,
				r.Text,
				ui.Bold.Render(hostPort(r.Text, r.Want)),
				ui.Dim.Render(fmt.Sprintf("%s:%d  (%s)", r.File, r.Line, how)),
			)
			fixed++
		}
	}

	for _, r := range dedupeAmbiguous(ambiguous) {
		ui.Warn("%s — %s:%d points at :%d, declared by %s",
			r.Service, r.File, r.Line, r.Port, strings.Join(r.Ambiguous, ", "))
		ui.Note("    say which in its manifest, and wharf keeps it right from then on:")
		ui.Note("      links: { %s: <service> }", r.Key)
	}
	for _, s := range dedupe(skipped) {
		ui.Note("skipped %s", s)
	}

	switch {
	case fixed == 0:
		ui.Ok("every cross-service URL already points at the right port")
	case dryRun:
		ui.Note("\ndry run — %d URLs would change, none were written", fixed)
	default:
		ui.Ok("\nrepointed %d URLs", fixed)
	}
	return nil
}

// hostPort swaps the port in a host:port token.
func hostPort(text string, port int) string {
	host := strings.Split(text, ":")[0]
	return fmt.Sprintf("%s:%d", host, port)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dedupeAmbiguous(in []link.Resolution) []link.Resolution {
	seen := map[string]bool{}
	var out []link.Resolution
	for _, r := range in {
		key := fmt.Sprintf("%s:%d:%d", r.File, r.Line, r.Port)
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}
