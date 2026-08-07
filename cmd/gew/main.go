package main

import (
	"fmt"
	"os"

	"gew/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gew: %v\n", err)
		os.Exit(1)
	}
}
