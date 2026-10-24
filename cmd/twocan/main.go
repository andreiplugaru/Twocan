// Command twocan is the Twocan desktop app.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"twocan/internal/core"
	"twocan/internal/logging"
	"twocan/internal/ui"
)

func main() { os.Exit(run()) }

// splitList splits a comma-separated flag value; "" gives an empty, non-nil
// list. (Same as twocan-cli's; too small to share a package for.)
func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func run() int {
	dataDir := flag.String("data-dir", "", "data directory (default: user config dir/twocan)")
	bootstrap := flag.String("bootstrap", "", "comma-separated bootstrap multiaddrs; empty for none")
	logLevel := flag.String("log-level", "info", "log file detail: debug, info, warn or error")
	flag.Parse()
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	opts := core.Options{DataDir: *dataDir, EnableMDNS: true}
	// An explicitly empty --bootstrap means none; unset means the default.
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "bootstrap" {
			opts.Bootstrap = splitList(*bootstrap)
		}
	})
	if opts.DataDir == "" {
		dir, err := core.DefaultDataDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		opts.DataDir = dir
	}

	l, err := logging.Open(opts.DataDir, level)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer l.Close()
	fmt.Fprintln(os.Stderr, "logging to", l.Path)
	opts.Log = l.Logger

	node, err := core.Start(context.Background(), opts)
	if err != nil {
		l.Error("start failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	ui.Run(node) // returns when the window is closed
	if err := node.Stop(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
