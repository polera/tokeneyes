package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/polera/tokeneyes/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.New(version).Execute(ctx, os.Args[1:]))
}
