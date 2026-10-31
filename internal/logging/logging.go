// Package logging writes Twocan's log file: our own structured events plus
// libp2p's internal logs and anything libraries print with the standard log
// package, all in one rotated file in the data dir.
//
// Never log message text or private keys. Peer IDs and IP addresses are
// fine: the file stays on the user's machine and full IDs let two devices'
// logs be matched up.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	golog "github.com/ipfs/go-log/v2"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	// FileName is the log file's name inside the data dir.
	FileName = "twocan.log"
	// The file is rotated at maxSizeMB, keeping maxBackups old files.
	maxSizeMB  = 5
	maxBackups = 3
)

// ParseLevel parses a --log-level value: debug, info, warn or error.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("unknown log level %q (want debug, info, warn or error)", s)
}

// Discard returns a logger that drops everything; the default for packages
// that were not given one.
func Discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// Log is an open log file.
type Log struct {
	*slog.Logger
	// Path is the log file's full path.
	Path string

	file      *lumberjack.Logger
	pipe      *golog.PipeReader
	copied    chan struct{}
	prevGolog golog.Config
	closeOnce sync.Once
	closeErr  error
}

// Open starts logging to <dataDir>/twocan.log at level. It also redirects
// the standard log package and libp2p's logs (warnings and up; at debug,
// info and up) into the same file, instead of the terminal.
func Open(dataDir string, level slog.Level) (*Log, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("logging: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, FileName)
	file := &lumberjack.Logger{Filename: path, MaxSize: maxSizeMB, MaxBackups: maxBackups}
	// lumberjack creates the file lazily; create it now so a bad path fails here.
	if _, err := file.Write(nil); err != nil {
		return nil, fmt.Errorf("logging: open %s: %w", path, err)
	}

	l := &Log{
		Logger:    slog.New(slog.NewTextHandler(file, &slog.HandlerOptions{Level: level})),
		Path:      path,
		file:      file,
		copied:    make(chan struct{}),
		prevGolog: golog.GetConfig(),
	}

	libLevel := golog.LevelWarn
	if level <= slog.LevelDebug {
		libLevel = golog.LevelInfo
	}
	// No stderr/stdout/file outputs of its own: libp2p logs reach us only
	// through the pipe below.
	golog.SetupLogging(golog.Config{Format: golog.PlaintextOutput, Level: libLevel})
	l.pipe = golog.NewPipeReader(golog.PipeFormat(golog.PlaintextOutput), golog.PipeLevel(golog.LevelDebug))
	go func() {
		defer close(l.copied)
		_, _ = io.Copy(file, l.pipe)
	}()

	log.SetOutput(file)
	return l, nil
}

// Close stops capturing library logs, restores their previous outputs and
// closes the file. It is safe to call more than once.
func (l *Log) Close() error {
	l.closeOnce.Do(func() {
		log.SetOutput(os.Stderr)
		pipeErr := l.pipe.Close()
		<-l.copied
		golog.SetupLogging(l.prevGolog)
		l.closeErr = errors.Join(pipeErr, l.file.Close())
	})
	return l.closeErr
}

// Recent returns the end of the log in dataDir: the newest rotated file
// followed by the current one, keeping the last max bytes. A missing file
// counts as empty.
func Recent(dataDir string, max int64) ([]byte, error) {
	base := strings.TrimSuffix(FileName, filepath.Ext(FileName))
	backups, err := filepath.Glob(filepath.Join(dataDir, base+"-*"+filepath.Ext(FileName)))
	if err != nil {
		return nil, err
	}
	sort.Strings(backups) // lumberjack names them by time, so the last is newest
	var files []string
	if len(backups) > 0 {
		files = append(files, backups[len(backups)-1])
	}
	files = append(files, filepath.Join(dataDir, FileName))
	var out []byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, b...)
	}
	if int64(len(out)) > max {
		out = out[int64(len(out))-max:]
	}
	return out, nil
}
