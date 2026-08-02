package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/setup"
)

// runSetup implements `coordinator setup` with non-interactive flags and an
// interactive fallback for anything still missing.
func runSetup(args []string) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "usage: coordinator setup [options]\n")
		_, _ = fmt.Fprintf(flags.Output(), "Provisions the coordinator database, schema, and local .env settings.\n\n")
		flags.PrintDefaults()
	}
	var (
		databaseURL = flags.String("db", "", "coordinator database URL (default: DATABASE_URL)")
		adminURL    = flags.String("admin-db", "", "maintenance URL to create a missing database (default: same host, 'postgres' db)")
		envFile     = flags.String("env-file", "", "settings file to write (default: .env)")
		force       = flags.Bool("force", false, "overwrite an existing settings file")
		yes         = flags.Bool("yes", false, "non-interactive: use defaults, fail on anything missing")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("setup takes no positional arguments")
	}

	databaseURLValue := *databaseURL
	if databaseURLValue == "" {
		databaseURLValue = os.Getenv("DATABASE_URL")
	}
	envFileValue := *envFile
	if envFileValue == "" {
		envFileValue = os.Getenv("ENV_FILE")
	}
	adminURLValue := *adminURL
	if adminURLValue == "" {
		adminURLValue = os.Getenv("POSTGRES_ADMIN_URL")
	}

	options := setup.Options{
		DatabaseURL:      databaseURLValue,
		AdminDatabaseURL: adminURLValue,
		EnvFile:          envFileValue,
		Force:            *force,
		Yes:              *yes,
		ConnectTimeout:   10 * time.Second,
		Out:              os.Stdout,
		In:               os.Stdin,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	log.Info("setup started", "db", setup.SanitizeDatabaseURL(databaseURLValue), "env_file", envFileValue)

	summary, err := setup.Run(ctx, options)
	if err != nil {
		log.Error("setup failed", "err", err)
		return err
	}
	_, _ = fmt.Fprint(os.Stdout, summary)
	log.Info("setup complete")
	return nil
}
