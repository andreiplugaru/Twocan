//go:build netlab

package netlab

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	// settle is how long the nodes run before the network changes: the
	// phone has been open a while, with its relay reservation in place.
	settle = 30 * time.Second
	// deliverWithin is how long the lab waits for a message after a switch.
	deliverWithin = 5 * time.Minute
	// target is how long a message should take after a switch: the phone
	// notices the change, drops the dead connections and reconnects,
	// through the relay if need be.
	target = 15 * time.Second
)

// world is a helper, a desktop and a phone that are contacts, connected
// over the home Wi-Fi.
type world struct {
	helper, desktop, phone *proc
}

func newWorld(t *testing.T) *world {
	t.Helper()
	if err := os.MkdirAll(os.Getenv("NETLAB_LOGS"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory per run: with -count, each run starts with new identities.
	logs, err := os.MkdirTemp(os.Getenv("NETLAB_LOGS"), t.Name()+"-")
	if err != nil {
		t.Fatal(err)
	}
	buildNet(t)
	t.Cleanup(func() {
		for _, ns := range []string{"inet", "helper", "home", "carrier", "desktop", "phone"} {
			_ = osexec.Command("ip", "netns", "del", ns).Run()
		}
	})
	bin := os.Getenv("NETLAB_BIN")
	listen := "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1"

	w := &world{}
	w.helper = start(t, "helper", "helper", filepath.Join(bin, "twocan-helper"),
		"--key", filepath.Join(logs, "helper.key"), "--listen", listen)
	var boot []string
	for from := 0; len(boot) < 2; {
		l, i, ok := w.helper.wait(regexp.MustCompile(`^/ip4/11\.0\.0\.10/.*/p2p/`), from, 10*time.Second)
		if !ok {
			t.Fatal("the helper printed no address")
		}
		boot, from = append(boot, l.text), i+1
	}
	node := func(name, ns string, mdns bool) *proc {
		return start(t, name, ns, filepath.Join(bin, "twocan-cli"),
			"--data-dir", filepath.Join(logs, name), "--bootstrap", strings.Join(boot, ","),
			"--listen", listen, fmt.Sprintf("--mdns=%t", mdns), "--name", name, "--log-level", "debug")
	}
	w.desktop = node("Desk", "desktop", true)
	w.phone = node("Phone", "phone", false) // Android has no mDNS
	mustWait(t, w.desktop, `^you are Desk`, 10*time.Second)
	mustWait(t, w.phone, `^you are Phone`, 10*time.Second)

	w.desktop.send(t, "/card")
	card := regexp.MustCompile(`twocan:[a-z0-9]+`).FindString(mustWait(t, w.desktop, `^twocan:`, 10*time.Second).text)
	w.phone.send(t, "/add "+card)
	mustWait(t, w.desktop, `^\* contact Phone is now`, 30*time.Second)
	w.desktop.send(t, "/accept Phone")
	mustWait(t, w.phone, `^\* Desk online`, 30*time.Second)
	// The desktop prints no "online": the phone connected before it was a
	// contact there.
	mustWait(t, w.desktop, `^\* contact Phone is now accepted`, 30*time.Second)
	t.Logf("contacts, connected over the home Wi-Fi; settling for %v", settle)
	time.Sleep(settle)
	return w
}

// measure sends a message each way right after change, and reports how
// long each took to arrive and what happened to the connections meanwhile.
func (w *world) measure(t *testing.T, label string, change func(*testing.T)) {
	t.Helper()
	dm, pm := w.desktop.mark(), w.phone.mark()
	t0 := time.Now()
	change(t)
	tag := fmt.Sprintf("%d", t0.UnixNano()%1e6)
	w.desktop.send(t, "/msg Phone d2p-"+tag)
	w.phone.send(t, "/msg Desk p2d-"+tag)

	got := func(p *proc, from int, pattern string) string {
		l, _, ok := p.wait(regexp.MustCompile(pattern), from, deliverWithin)
		if !ok {
			t.Errorf("%s: %s: not delivered within %v", label, pattern, deliverWithin)
			return "not delivered"
		}
		took := l.at.Sub(t0)
		if took > target {
			t.Errorf("%s: %s took %v, over the %v target", label, pattern, took.Round(100*time.Millisecond), target)
		}
		return took.Round(100 * time.Millisecond).String()
	}
	toPhone := got(w.phone, pm, `^<- Desk: d2p-`+tag)
	toDesk := got(w.desktop, dm, `^<- Phone: p2d-`+tag)
	t.Logf("%s: desktop -> phone %s, phone -> desktop %s", label, toPhone, toDesk)
	for _, p := range []struct {
		name string
		proc *proc
		from int
	}{{"desktop", w.desktop, dm}, {"phone", w.phone, pm}} {
		for _, l := range p.proc.since(p.from) {
			if strings.HasPrefix(l.text, "* ") {
				t.Logf("  %-7s +%-6v %s", p.name, l.at.Sub(t0).Round(100*time.Millisecond), l.text)
			}
		}
	}
}

// The phone leaves the Wi-Fi it shares with the desktop for mobile data.
func TestWifiToMobile(t *testing.T) {
	w := newWorld(t)
	w.measure(t, "wifi -> mobile", onMobile)
}

// The phone goes to mobile data and, once that works, back to Wi-Fi.
func TestMobileToWifi(t *testing.T) {
	w := newWorld(t)
	w.measure(t, "wifi -> mobile", onMobile)
	time.Sleep(settle)
	w.measure(t, "mobile -> wifi", onWifi)
}
