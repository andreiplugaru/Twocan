// Command twocan-cli is a text client for Twocan.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"twocan/internal/cli"
	"twocan/internal/core"
	"twocan/internal/logging"
)

func main() { os.Exit(run()) }

// splitList splits a comma-separated flag value; "" gives an empty, non-nil list.
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
	listen := flag.String("listen", "", "comma-separated listen multiaddrs; empty for none")
	mdns := flag.Bool("mdns", true, "discover peers on the local network")
	name := flag.String("name", "", "display name (required on first run)")
	logLevel := flag.String("log-level", "info", "log file detail: debug, info, warn or error")
	flag.Parse()
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}

	opts := core.Options{DataDir: *dataDir, EnableMDNS: *mdns}
	// An explicitly empty --bootstrap/--listen means none; unset means default.
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "bootstrap":
			opts.Bootstrap = splitList(*bootstrap)
		case "listen":
			opts.ListenAddrs = splitList(*listen)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	node, err := core.Start(ctx, opts)
	if err != nil {
		l.Error("start failed", "err", err)
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer node.Stop()

	if *name != "" {
		if err := node.SetDisplayName(*name); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 2
		}
	}
	if node.DisplayName() == "" {
		fmt.Fprintln(os.Stderr, "error: set a display name with --name")
		return 2
	}

	fmt.Printf("you are %s (%s); /help for commands\n", node.DisplayName(), node.PeerID())
	if err := cli.Run(ctx, node, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
