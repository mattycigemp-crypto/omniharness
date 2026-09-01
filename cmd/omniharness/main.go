// Command omniharness is the agent harness that runs above OmniRoute.
package main

import (
	"fmt"
	"os"

	"omniharness/internal/cli"
)

func main() {
	root := cli.NewRootCmd()
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
