package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/polera/tokeneyes/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.New().Execute(ctx, os.Args[1:]))
}
