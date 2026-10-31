// Package logbox is log reports: the helper's store of them, the protocols
// devices send them over and readers fetch them with, and the clients.
package logbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	pb "twocan/internal/proto"
)

var (
	// ErrTooLarge is returned by Upload.Write past Limits.MaxUpload.
	ErrTooLarge = errors.New("log is too large")
	// ErrFull is returned when a report cannot be stored.
	ErrFull = errors.New("log storage is full")
	// ErrNotFound is returned for an unknown report code.
	ErrNotFound = errors.New("no such report")
)

const (
	offFile    = "OFF"
	tempPrefix = ".upload-"
	// tempGrace is how old a temporary file must be before Prune treats it
	// as left over: an upload in progress is never older than its idle
	// timeouts allow.
	tempGrace = 10 * time.Minute
)

// Limits bounds the store.
type Limits struct {
	MaxUpload int64         // bytes in one report
	MaxTotal  int64         // bytes in all reports; the oldest go first
	MaxAge    time.Duration // reports older than this are deleted
}

// DefaultLimits are 8 MiB per report, 256 MiB in all, kept 7 days.
func DefaultLimits() Limits {
	return Limits{MaxUpload: 8 << 20, MaxTotal: 256 << 20, MaxAge: 7 * 24 * time.Hour}
}

// Store keeps reports in a directory: <received>-<code>.log.gz with the log
// as sent and <received>-<code>.json with its ReportInfo. Nothing a device
// sends becomes part of a file name.
type Store struct {
	dir string
	lim Limits
	mu  sync.Mutex // serialises commits, deletes and pruning
}

// OpenStore opens (creating, mode 0700) the store in dir.
func OpenStore(dir string, lim Limits) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("log store: %w", err)
	}
	return &Store{dir: dir, lim: lim}, nil
}

// Off reports whether receiving is switched off: the file OFF exists.
func (s *Store) Off() bool {
	_, err := os.Stat(filepath.Join(s.dir, offFile))
	return err == nil
}

// SetOff switches receiving off (creating OFF) or on (removing it).
func (s *Store) SetOff(off bool) error {
	p := filepath.Join(s.dir, offFile)
	if off {
		return os.WriteFile(p, nil, 0o600)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Upload is a report being received, in a temporary file.
type Upload struct {
	s *Store
	f *os.File
	n int64
}

// Begin starts receiving a report.
func (s *Store) Begin() (*Upload, error) {
	f, err := os.CreateTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return nil, err
	}
	return &Upload{s: s, f: f}, nil
}

// Write appends to the report; past MaxUpload it writes nothing and returns
// ErrTooLarge.
func (u *Upload) Write(p []byte) (int, error) {
	if u.n+int64(len(p)) > u.s.lim.MaxUpload {
		return 0, ErrTooLarge
	}
	n, err := u.f.Write(p)
	u.n += int64(n)
	return n, err
}

// Abort discards the upload.
func (u *Upload) Abort() {
	u.f.Close()
	os.Remove(u.f.Name())
}

// Commit stores the upload as a report received at now, deleting the oldest
// reports if it would not fit otherwise. It fills in info's code, received
// time and size, and returns the code.
func (u *Upload) Commit(info *pb.ReportInfo, now time.Time) (string, error) {
	if err := u.f.Close(); err != nil {
		os.Remove(u.f.Name())
		return "", err
	}
	s := u.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if u.n > s.lim.MaxTotal {
		os.Remove(u.f.Name())
		return "", ErrFull
	}
	if err := s.makeRoomLocked(u.n); err != nil {
		os.Remove(u.f.Name())
		return "", err
	}
	info.Code = newCode()
	info.ReceivedMs = now.UnixMilli()
	info.Size = uint64(u.n)
	stem := filepath.Join(s.dir, now.UTC().Format("20060102T150405Z")+"-"+info.Code)
	if err := os.Rename(u.f.Name(), stem+".log.gz"); err != nil {
		os.Remove(u.f.Name())
		return "", err
	}
	js, err := protojson.Marshal(info)
	if err == nil {
		err = os.WriteFile(stem+".json", js, 0o600)
	}
	if err != nil {
		os.Remove(stem + ".log.gz")
		os.Remove(stem + ".json")
		return "", err
	}
	return info.Code, nil
}

// makeRoomLocked deletes the oldest reports until size more bytes fit.
func (s *Store) makeRoomLocked(size int64) error {
	rs, err := s.listLocked()
	if err != nil {
		return err
	}
	var total int64
	for _, r := range rs {
		total += int64(r.info.Size)
	}
	for _, r := range rs {
		if total+size <= s.lim.MaxTotal {
			break
		}
		s.removeLocked(r.stem)
		total -= int64(r.info.Size)
	}
	return nil
}

// stored is a report on disk: its path without extension, and its details.
type stored struct {
	stem string
	info *pb.ReportInfo
}

// listLocked returns the reports, oldest first; unreadable ones are skipped.
func (s *Store) listLocked() ([]stored, error) {
	es, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []stored
	for _, e := range es {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			continue
		}
		var info pb.ReportInfo
		if protojson.Unmarshal(b, &info) != nil || !validCode(info.Code) {
			continue
		}
		out = append(out, stored{filepath.Join(s.dir, strings.TrimSuffix(name, ".json")), &info})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].info.ReceivedMs != out[j].info.ReceivedMs {
			return out[i].info.ReceivedMs < out[j].info.ReceivedMs
		}
		return out[i].stem < out[j].stem
	})
	return out, nil
}

func (s *Store) removeLocked(stem string) {
	os.Remove(stem + ".log.gz")
	os.Remove(stem + ".json")
}

// List returns the reports' details, oldest first.
func (s *Store) List() ([]*pb.ReportInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, err := s.listLocked()
	if err != nil {
		return nil, err
	}
	out := make([]*pb.ReportInfo, len(rs))
	for i, r := range rs {
		out[i] = r.info
	}
	return out, nil
}

func (s *Store) findLocked(code string) (stored, error) {
	if !validCode(code) {
		return stored{}, ErrNotFound
	}
	rs, err := s.listLocked()
	if err != nil {
		return stored{}, err
	}
	for _, r := range rs {
		if r.info.Code == code {
			return r, nil
		}
	}
	return stored{}, ErrNotFound
}

// Open returns the log of report code (as sent: gzipped) and its details.
func (s *Store) Open(code string) (io.ReadCloser, *pb.ReportInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.findLocked(code)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(r.stem + ".log.gz")
	if err != nil {
		return nil, nil, err
	}
	return f, r.info, nil
}

// Delete removes report code.
func (s *Store) Delete(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.findLocked(code)
	if err != nil {
		return err
	}
	s.removeLocked(r.stem)
	return nil
}

// Prune deletes reports older than MaxAge and temporary files left over by
// uploads that never finished (a crash).
func (s *Store) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	es, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > tempGrace {
			os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
	rs, err := s.listLocked()
	if err != nil {
		return err
	}
	for _, r := range rs {
		if now.Sub(time.UnixMilli(r.info.ReceivedMs)) > s.lim.MaxAge {
			s.removeLocked(r.stem)
		}
	}
	return nil
}
