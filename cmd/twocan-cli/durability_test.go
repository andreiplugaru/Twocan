//go:build integration

package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// proc is one running twocan-cli whose stdin stays open for its whole life
// (Run returns on stdin EOF and would drop in-flight events).
type proc struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser

	mu    sync.Mutex
	lines []string
	done  chan struct{}
}

func startProc(t *testing.T, bin string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin, args...)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	p := &proc{t: t, cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		for sc.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, sc.Text())
			p.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	return p
}

func (p *proc) send(format string, args ...any) {
	p.t.Helper()
	_, err := fmt.Fprintf(p.stdin, format+"\n", args...)
	require.NoError(p.t, err)
}

func (p *proc) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lines...)
}

// count returns how many output lines contain sub.
func (p *proc) count(sub string) int {
	n := 0
	for _, l := range p.snapshot() {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

// waitLine waits for a line containing sub at index >= from and returns it.
func (p *proc) waitLine(sub string, from int, timeout time.Duration) string {
	p.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ls := p.snapshot()
		for i := from; i < len(ls); i++ {
			if strings.Contains(ls[i], sub) {
				return ls[i]
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.t.Fatalf("timed out waiting for %q; output:\n%s", sub, strings.Join(p.snapshot(), "\n"))
	return ""
}

// run sends cmd and returns the first output line after it containing sub.
func (p *proc) run(cmd, sub string, timeout time.Duration) string {
	p.t.Helper()
	from := len(p.snapshot())
	p.send("%s", cmd)
	return p.waitLine(sub, from, timeout)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

var statsRe = regexp.MustCompile(`in=(\d+) out=(\d+) pending=(\d+)`)

func TestKillMinus9LosesNothing(t *testing.T) {
	const total = 200

	bin := filepath.Join(t.TempDir(), "twocan-cli")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))

	dirA, dirB := t.TempDir(), t.TempDir()
	// B reuses its first port after the restart: with no mDNS and no DHT
	// nothing would let A learn a new one.
	listenB := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", freePort(t))

	common := []string{"--mdns=false", "--bootstrap", ""}
	aArgs := append([]string{"--data-dir", dirA, "--name", "alice", "--listen", "/ip4/127.0.0.1/tcp/0"}, common...)
	bArgs := append([]string{"--data-dir", dirB, "--name", "bob", "--listen", listenB}, common...)
	a := startProc(t, bin, aArgs...)
	b := startProc(t, bin, bArgs...)

	// A adds B's card; B sees the request and accepts.
	cardB := b.run("/card", "twocan:", 10*time.Second)
	a.run("/add "+strings.TrimSpace(cardB), "added", 10*time.Second)
	b.waitLine("alice is now pending_incoming", 0, 10*time.Second)
	b.run("/accept alice", "ok", 10*time.Second)

	for i := 0; i < total; i++ {
		a.send("/msg bob m-%03d", i)
	}

	deadline := time.Now().Add(30 * time.Second)
	for b.count("<- alice: m-") < 50 {
		require.True(t, time.Now().Before(deadline), "B never received 50 messages:\n%s", strings.Join(b.snapshot(), "\n"))
		time.Sleep(2 * time.Millisecond)
	}
	require.NoError(t, syscall.Kill(b.cmd.Process.Pid, syscall.SIGKILL))
	_ = b.cmd.Wait()
	<-b.done
	before := b.count("<- alice: m-")
	t.Logf("B printed %d messages before SIGKILL", before)
	require.Less(t, before, total, "B received everything before the kill; crash path not exercised")

	// The kill must have landed mid-transfer: A still has undelivered messages.
	st := a.run("/stats bob", "in=", 10*time.Second)
	m := statsRe.FindStringSubmatch(st)
	require.NotNil(t, m, st)
	require.NotEqual(t, "0", m[3], "A had no pending messages after the kill; crash path not exercised: %s", st)

	b = startProc(t, bin, bArgs...)

	// A's reconnect loop redelivers; poll until nothing is pending.
	deadline = time.Now().Add(60 * time.Second)
	for {
		last := a.run("/stats bob", "in=", 10*time.Second)
		if m := statsRe.FindStringSubmatch(last); m != nil && m[3] == "0" {
			break
		}
		require.True(t, time.Now().Before(deadline), "A still has pending messages: %s", last)
		time.Sleep(time.Second)
	}

	bStats := b.run("/stats alice", "in=", 10*time.Second)
	require.Contains(t, bStats, fmt.Sprintf("in=%d ", total))

	from := len(b.snapshot())
	b.send("/history alice")
	time.Sleep(time.Second)
	hist := b.snapshot()[from:]
	for i := 0; i < total; i++ {
		// History lines end with the text for incoming messages, e.g.
		// "[15:04] 3fa2b1c0 alice: m-007".
		needle := fmt.Sprintf(" alice: m-%03d", i)
		n := 0
		for _, l := range hist {
			if strings.HasSuffix(strings.TrimSpace(l), needle) {
				n++
			}
		}
		require.Equal(t, 1, n, "m-%03d appears %d times in B's history", i, n)
	}

	a.send("/quit")
	b.send("/quit")
}
