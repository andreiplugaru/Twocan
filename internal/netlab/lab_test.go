//go:build netlab

package netlab

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestMain builds the binaries, then runs the tests again inside a new user,
// network and mount namespace, where we may create network namespaces.
func TestMain(m *testing.M) {
	if os.Getenv("NETLAB_INSIDE") == "" {
		os.Exit(outside())
	}
	// ip netns keeps its namespaces in /run/netns: give it a private /run.
	if err := syscall.Mount("none", "/run", "tmpfs", 0, ""); err != nil {
		fmt.Fprintln(os.Stderr, "netlab: mount /run:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll("/run/netns", 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "netlab:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func outside() int {
	bin, err := os.MkdirTemp("", "netlab-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "netlab:", err)
		return 1
	}
	defer os.RemoveAll(bin)
	for _, c := range []string{"twocan-cli", "twocan-helper"} {
		build := exec.Command("go", "build", "-o", filepath.Join(bin, c), "twocan/cmd/"+c)
		build.Stdout, build.Stderr = os.Stdout, os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "netlab: build", c+":", err)
			return 1
		}
	}
	logs := os.Getenv("NETLAB_LOGS")
	if logs == "" {
		logs = filepath.Join(os.TempDir(), "netlab-"+time.Now().Format("20060102-150405"))
	}
	inner := exec.Command("unshare", append([]string{"-rnm", "--propagation", "private", os.Args[0]}, os.Args[1:]...)...)
	inner.Env = append(os.Environ(), "NETLAB_INSIDE=1", "NETLAB_BIN="+bin, "NETLAB_LOGS="+logs)
	inner.Stdin, inner.Stdout, inner.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = inner.Run()
	fmt.Println("netlab: node logs in", logs)
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "netlab:", err)
		return 1
	}
	return 0
}

// sh runs a command, failing the test if it fails.
func sh(t *testing.T, name string, args ...string) {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

// in runs ip with -n ns (or nft/tc/sysctl inside ns).
func in(t *testing.T, ns, cmd string, args ...string) {
	t.Helper()
	if cmd == "ip" {
		sh(t, "ip", append([]string{"-n", ns}, args...)...)
		return
	}
	sh(t, "ip", append([]string{"netns", "exec", ns, cmd}, args...)...)
}

var vethSeq int

// veth links ifA in nsA to ifB in nsB.
func veth(t *testing.T, nsA, ifA, nsB, ifB string) {
	t.Helper()
	vethSeq++
	a, b := fmt.Sprintf("va%d", vethSeq), fmt.Sprintf("vb%d", vethSeq)
	sh(t, "ip", "link", "add", "name", a, "type", "veth", "peer", "name", b)
	sh(t, "ip", "link", "set", a, "netns", nsA)
	sh(t, "ip", "link", "set", b, "netns", nsB)
	in(t, nsA, "ip", "link", "set", a, "name", ifA)
	in(t, nsB, "ip", "link", "set", b, "name", ifB)
}

// net is the lab's networks (see the package doc):
//
//	inet 11.0.0.0/24: helper .10, home router .20, carrier router .30
//	home LAN 192.168.1.0/24: router .1, desktop .10, phone wlan0 .20
//	carrier LAN 100.64.0.0/24: router .1, phone rmnet0 .5
//
// The home router's NAT keeps ports where it can (hole punching works);
// the carrier's picks random ports, like carrier-grade NAT (it does not).
func buildNet(t *testing.T) {
	t.Helper()
	for _, ns := range []string{"inet", "helper", "home", "carrier", "desktop", "phone"} {
		sh(t, "ip", "netns", "add", ns)
		in(t, ns, "ip", "link", "set", "lo", "up")
	}
	in(t, "inet", "ip", "link", "add", "br0", "type", "bridge")
	in(t, "inet", "ip", "link", "set", "br0", "up")
	for _, h := range []struct{ ns, addr string }{{"helper", "11.0.0.10"}, {"home", "11.0.0.20"}, {"carrier", "11.0.0.30"}} {
		veth(t, h.ns, "wan", "inet", h.ns)
		in(t, "inet", "ip", "link", "set", "dev", h.ns, "master", "br0", "up")
		in(t, h.ns, "ip", "addr", "add", h.addr+"/24", "dev", "wan")
		in(t, h.ns, "ip", "link", "set", "wan", "up")
	}

	in(t, "home", "ip", "link", "add", "br1", "type", "bridge")
	in(t, "home", "ip", "addr", "add", "192.168.1.1/24", "dev", "br1")
	in(t, "home", "ip", "link", "set", "br1", "up")
	veth(t, "desktop", "eth0", "home", "desktop")
	veth(t, "phone", "wlan0", "home", "phone")
	for _, port := range []string{"desktop", "phone"} {
		in(t, "home", "ip", "link", "set", "dev", port, "master", "br1", "up")
	}
	in(t, "desktop", "ip", "addr", "add", "192.168.1.10/24", "dev", "eth0")
	in(t, "desktop", "ip", "link", "set", "eth0", "up")
	in(t, "desktop", "ip", "route", "add", "default", "via", "192.168.1.1")

	veth(t, "phone", "rmnet0", "carrier", "lan")
	in(t, "carrier", "ip", "addr", "add", "100.64.0.1/24", "dev", "lan")
	in(t, "carrier", "ip", "link", "set", "lan", "up")
	in(t, "phone", "ip", "addr", "add", "100.64.0.5/24", "dev", "rmnet0")
	in(t, "phone", "ip", "addr", "add", "192.168.1.20/24", "dev", "wlan0")
	onWifi(t)

	nat := func(ns, how string) {
		in(t, ns, "sysctl", "-qw", "net.ipv4.ip_forward=1")
		in(t, ns, "nft", "add", "table", "ip", "nat")
		in(t, ns, "nft", "add chain ip nat post { type nat hook postrouting priority srcnat ; }")
		in(t, ns, "nft", "add rule ip nat post oifname wan "+how)
	}
	nat("home", "masquerade")
	nat("carrier", "masquerade random,fully-random")
}

// onWifi puts the phone on Wi-Fi, with mobile data off.
func onWifi(t *testing.T) {
	t.Helper()
	in(t, "phone", "ip", "link", "set", "rmnet0", "down")
	in(t, "phone", "ip", "link", "set", "wlan0", "up")
	in(t, "phone", "ip", "route", "replace", "default", "via", "192.168.1.1")
}

// onMobile puts the phone on mobile data, with Wi-Fi off.
func onMobile(t *testing.T) {
	t.Helper()
	in(t, "phone", "ip", "link", "set", "wlan0", "down")
	in(t, "phone", "ip", "link", "set", "rmnet0", "up")
	in(t, "phone", "ip", "route", "replace", "default", "via", "100.64.0.1")
}

// line is one line a process printed, with when the lab read it.
type line struct {
	at   time.Time
	text string
}

// proc is a process running in a namespace whose stdout we watch.
type proc struct {
	name  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	lines []line
	more  chan struct{} // closed and replaced on each new line
}

func start(t *testing.T, name, ns string, args ...string) *proc {
	t.Helper()
	cmd := exec.Command("ip", append([]string{"netns", "exec", ns}, args...)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	p := &proc{name: name, cmd: cmd, stdin: stdin, more: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			p.mu.Lock()
			p.lines = append(p.lines, line{time.Now(), sc.Text()})
			close(p.more)
			p.more = make(chan struct{})
			p.mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	return p
}

// send writes a command line to the process.
func (p *proc) send(t *testing.T, s string) {
	t.Helper()
	if _, err := fmt.Fprintln(p.stdin, s); err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
}

// wait returns the first line from index from on matching re, waiting up
// to timeout; ok is false on timeout.
func (p *proc) wait(re *regexp.Regexp, from int, timeout time.Duration) (l line, idx int, ok bool) {
	deadline := time.After(timeout)
	for i := from; ; {
		p.mu.Lock()
		for ; i < len(p.lines); i++ {
			if re.MatchString(p.lines[i].text) {
				l := p.lines[i]
				p.mu.Unlock()
				return l, i, true
			}
		}
		more := p.more
		p.mu.Unlock()
		select {
		case <-more:
		case <-deadline:
			return line{}, i, false
		}
	}
}

// mark is the index the next line will have.
func (p *proc) mark() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.lines)
}

// since returns the lines from index from on.
func (p *proc) since(from int) []line {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]line(nil), p.lines[from:]...)
}

func mustWait(t *testing.T, p *proc, pattern string, timeout time.Duration) line {
	t.Helper()
	l, _, ok := p.wait(regexp.MustCompile(pattern), 0, timeout)
	if !ok {
		t.Fatalf("%s: no line matching %q within %v", p.name, pattern, timeout)
	}
	return l
}
