package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// runToken implements `coordinator token`: prints the worker auth token of a
// `coordinator serve` instance, so a scientist can join a worker without
// hunting through the data directory. The file itself is what serve created.
func runToken(args []string) error {
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(), "usage: coordinator token [options]\n")
		_, _ = fmt.Fprintf(flags.Output(), "Prints the WORKER_AUTH_TOKEN of this coordinator's serve instance.\n\n")
		flags.PrintDefaults()
	}
	var dataDir = flags.String("data-dir", defaultDataDir(), "data directory (default: ~/.scimesh)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("token takes no positional arguments")
	}
	token, err := os.ReadFile(filepath.Join(*dataDir, "worker.token"))
	if err != nil {
		return fmt.Errorf("no worker token found in %s — start the coordinator with `coordinator serve` first", *dataDir)
	}
	fmt.Print(string(token))
	return nil
}
