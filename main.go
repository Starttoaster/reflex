// Command reflex routes Alertmanager notifications to Discord using Jev.
package main

import (
	"fmt"
	"os"

	"github.com/starttoaster/reflex/cmd"
)

func main() {
	if err := cmd.NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
