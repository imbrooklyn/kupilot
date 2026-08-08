package main

import (
	"context"
	"io"
	"os"

	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, buildinfo.Read()))
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	info buildinfo.Info,
) int {
	return cli.Run(ctx, args, stdout, stderr, info, start)
}

func start(context.Context, cli.StartIntent) error {
	return cli.UnavailableError{}
}
