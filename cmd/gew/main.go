package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"gew/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "gew: %v\n", err)
		os.Exit(1)
	}
}
