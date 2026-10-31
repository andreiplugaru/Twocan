// Command twocan-logs reads the log reports devices sent to the helper.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"twocan/internal/identity"
	"twocan/internal/logbox"
	"twocan/internal/p2p"
)

// maxText is the most a report may decompress to.
const maxText = 256 << 20

const usage = `usage: twocan-logs [--helper ADDRS] [--key FILE] COMMAND

commands:
  init                         create the reader key and print its peer ID
  list                         list the reports on the helper
  get <code>|latest [-n N] [-o DIR]
                               download reports (latest: the newest N, default 1)
  delete <code>                delete a report
  off                          stop the helper accepting logs
  on                           let the helper accept logs again

The helper is --helper, else TWOCAN_BOOTSTRAP, else the one built in.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "twocan-logs:", err)
		os.Exit(1)
	}
}

func defaultKey() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "reader.key"
	}
	return filepath.Join(dir, "twocan-logs", "reader.key")
}

func run() error {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	helperFlag := flag.String("helper", "", "comma-separated helper multiaddrs (ending in /p2p/<id>)")
	keyPath := flag.String("key", defaultKey(), "the reader's key file")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		return errors.New("no command")
	}
	key, err := identity.LoadOrCreateKey(*keyPath)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		id, err := peer.IDFromPrivateKey(key)
		if err != nil {
			return err
		}
		fmt.Printf("reader key: %s\nreader peer ID: %s\n\n", *keyPath, id)
		fmt.Printf("On the helper, add it with `sudo systemctl edit twocan-helper`:\n\n"+
			"[Service]\nExecStart=\nExecStart=/usr/local/bin/twocan-helper --key /var/lib/twocan-helper/helper.key --log-readers %s\n", id)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	h, err := libp2p.New(libp2p.Identity(key), libp2p.NoListenAddrs)
	if err != nil {
		return err
	}
	defer h.Close()
	helper, err := connectHelper(ctx, h, *helperFlag)
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		rs, err := logbox.List(ctx, h, helper)
		if err != nil {
			return err
		}
		for _, r := range rs {
			fmt.Printf("%s  %s  %8d  %-20s %s\n", r.Code, time.UnixMilli(r.ReceivedMs).Local().Format("2006-01-02 15:04:05"),
				r.Size, r.Details.GetName(), r.Details.GetApp())
		}
		if len(rs) == 0 {
			fmt.Println("no reports")
		}
		return nil
	case "get":
		return get(ctx, h, helper, args[1:])
	case "delete":
		if len(args) != 2 {
			return errors.New("usage: twocan-logs delete <code>")
		}
		return logbox.Delete(ctx, h, helper, args[1])
	case "off", "on":
		if err := logbox.SetOff(ctx, h, helper, args[0] == "off"); err != nil {
			return err
		}
		fmt.Println("receiving logs:", args[0])
		return nil
	}
	flag.Usage()
	return fmt.Errorf("unknown command %q", args[0])
}

// connectHelper connects to the first helper that answers.
func connectHelper(ctx context.Context, h host.Host, flagAddrs string) (peer.ID, error) {
	var addrs []string
	switch {
	case flagAddrs != "":
		addrs = strings.Split(flagAddrs, ",")
	case os.Getenv("TWOCAN_BOOTSTRAP") != "":
		addrs = strings.Split(os.Getenv("TWOCAN_BOOTSTRAP"), ",")
	default:
		addrs = p2p.DefaultBootstrap
	}
	helpers, err := p2p.ParseBootstrap(addrs)
	if err != nil {
		return "", err
	}
	if len(helpers) == 0 {
		return "", errors.New("no helper: pass --helper or set TWOCAN_BOOTSTRAP")
	}
	var lastErr error
	for _, hp := range helpers {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		lastErr = h.Connect(cctx, hp)
		cancel()
		if lastErr == nil {
			return hp.ID, nil
		}
	}
	return "", fmt.Errorf("could not reach the helper: %w", lastErr)
}

func get(ctx context.Context, h host.Host, helper peer.ID, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	n := fs.Int("n", 1, "with latest: how many of the newest reports")
	out := fs.String("o", ".", "folder to write the reports to")
	if len(args) == 0 {
		return errors.New("usage: twocan-logs get <code>|latest [-n N] [-o DIR]")
	}
	which := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	codes := []string{which}
	if which == "latest" {
		rs, err := logbox.List(ctx, h, helper)
		if err != nil {
			return err
		}
		codes = nil
		for _, r := range rs[max(0, len(rs)-*n):] {
			codes = append(codes, r.Code)
		}
		if len(codes) == 0 {
			return errors.New("no reports")
		}
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for _, code := range codes {
		var gz bytes.Buffer
		info, err := logbox.Get(ctx, h, helper, code, &gz)
		if err != nil {
			return fmt.Errorf("%s: %w", code, err)
		}
		logPath, txtPath, err := logbox.WriteReport(*out, info, &gz, maxText)
		if err != nil {
			return fmt.Errorf("%s: %w", code, err)
		}
		fmt.Println(logPath)
		fmt.Println(txtPath)
	}
	return nil
}
