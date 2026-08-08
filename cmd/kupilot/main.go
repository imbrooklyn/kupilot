package main

import (
	"context"
	"io"
	"log/slog"
	"os"

	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	platformlogging "github.com/imbrooklyn/kupilot/internal/platform/logging"
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

func start(ctx context.Context, intent cli.StartIntent) error {
	paths, err := config.SystemPaths()
	if err != nil {
		return err
	}
	loaded, err := config.Load(ctx, config.LoadOptions{
		Paths: paths,
		Overrides: config.Overrides{
			ConfigFile: config.StringOverride{Set: intent.Options.ConfigFileSet, Value: intent.Options.ConfigFile},
			Context:    config.StringOverride{Set: intent.Options.ContextSet, Value: intent.Options.Context},
			Namespace:  config.StringOverride{Set: intent.Options.NamespaceSet, Value: intent.Options.Namespace},
			NoColor:    config.BoolOverride{Set: intent.Options.NoColorSet, Value: intent.Options.NoColor},
		},
	})
	if err != nil {
		return err
	}
	if loaded.Model.Endpoint == "" && loaded.Model.Model == "" {
		return cli.UnavailableError{}
	}
	model, err := loaded.ValidatedModel()
	if err != nil {
		return err
	}
	secretSource := new(config.EnvironmentSecretSource)
	secret, err := secretSource.Read()
	if err != nil {
		return err
	}
	defer secret.Destroy()

	if loaded.Logging.Enabled {
		sink, err := platformlogging.Open(ctx, platformlogging.Options{
			Directory: loaded.Paths.LogDir,
			Level:     configuredLogLevel(loaded.Logging.Level),
		})
		if err != nil {
			return err
		}
		sink.Logger.InfoContext(ctx, platformlogging.EventStartup,
			"component", "composition",
			"operation", "configuration_load",
			"outcome", "success",
			"provider_kind", model.ProviderKind,
		)
		if err := sink.Close(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return cli.UnavailableError{}
}

func configuredLogLevel(value string) slog.Level {
	switch value {
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
