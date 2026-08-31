package main

import (
	"fmt"
	"os"

	"github.com/Leonz3n/devtask/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "devtask: %v\n", err)
		os.Exit(cli.ExitCode(err))
	}
}
