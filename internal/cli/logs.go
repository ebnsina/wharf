package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ebnsina/wharf/internal/manifest"
	"github.com/ebnsina/wharf/internal/ui"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var follow bool
	var tail int
	var procName string

	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Show what a service printed",
		Long: "Reads the log wharf persisted for the service's last run. Defaults to the\n" +
			"project you are standing in.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(args, procName, tail, follow)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing as new lines arrive")
	cmd.Flags().IntVarP(&tail, "tail", "n", 200, "how many lines to show (0 for all)")
	cmd.Flags().StringVar(&procName, "process", "", "which process, when a service runs several (api, worker)")
	return cmd
}

func runLogs(args []string, procName string, tail int, follow bool) error {
	st, err := store()
	if err != nil {
		return err
	}
	services, err := st.LoadServices()
	if err != nil {
		return err
	}
	svc, err := requireService(services, args)
	if err != nil {
		return err
	}

	proc, err := pickProcess(svc, procName)
	if err != nil {
		return err
	}

	path := st.LogPath(svc.Name, proc)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no log for %s — wharf has not run it yet", svc.Name)
		}
		return err
	}
	defer f.Close()

	if tail > 0 {
		if err := printTail(f, tail); err != nil {
			return err
		}
	} else {
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
	}

	if !follow {
		return nil
	}

	ui.Note("— following %s; ctrl-c to stop —", filepath.Base(path))
	return followFile(f)
}

// pickProcess chooses which of a service's processes to read.
func pickProcess(svc manifest.Service, want string) (string, error) {
	if want != "" {
		for _, p := range svc.Processes {
			if p.Name == want {
				return want, nil
			}
		}
		return "", fmt.Errorf("%s has no process called %q", svc.Name, want)
	}

	for _, p := range svc.Processes {
		if p.Primary {
			return p.Name, nil
		}
	}
	if len(svc.Processes) > 0 {
		return svc.Processes[0].Name, nil
	}
	return "", fmt.Errorf("%s has no processes", svc.Name)
}

// printTail writes the last n lines.
//
// The whole file is scanned rather than seeking backwards from the end: these
// are dev logs from a single run, not gigabyte archives, and the simple version
// cannot land mid-character in a multi-byte rune.
func printTail(f *os.File, n int) error {
	ring := make([]string, 0, n)

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		if len(ring) == n {
			ring = ring[1:]
		}
		ring = append(ring, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	for _, line := range ring {
		fmt.Println(colorLog(line))
	}
	return nil
}

// followFile prints lines as they are appended, like tail -f.
func followFile(f *os.File) error {
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			// Nothing new. Sleeping beats spinning on a file that a compiling
			// service will not write to for another ten seconds.
			time.Sleep(300 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
		fmt.Print(colorLog(strings.TrimRight(line, "\n")) + "\n")
	}
}

// colorLog tints a line by severity, so an error is findable in a wall of
// output.
func colorLog(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "ERROR"), strings.Contains(upper, "FATAL"), strings.Contains(upper, "PANIC"):
		return ui.Red.Render(line)
	case strings.Contains(upper, "WARN"):
		return ui.Yellow.Render(line)
	case strings.HasPrefix(line, "\t"), strings.HasPrefix(line, "    /"):
		return ui.Dim.Render(line)
	default:
		return line
	}
}
