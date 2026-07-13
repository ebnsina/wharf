// Command wharf runs your local services without you having to remember how.
package main

import (
	"fmt"
	"os"

	"github.com/ebnsina/wharf/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wharf:", err)
		os.Exit(1)
	}
}
