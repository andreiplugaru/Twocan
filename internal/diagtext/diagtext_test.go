package diagtext

import (
	"errors"
	"testing"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/core"
	"twocan/internal/p2p"
	"twocan/internal/store"
)

var now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local)

func fields(fs []Field) map[string]string {
	m := make(map[string]string, len(fs))
	for _, f := range fs {
		m[f.Label] = f.Value
	}
	return m
}

func TestDetailsConnected(t *testing.T) {
	d := core.ContactDetails{
		Contact: store.Contact{Addrs: []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.5/tcp/4242")}},
		Conns: []p2p.Conn{
			{Remote: ma.StringCast("/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWHxukoUg4QZmSWciogRpYmRaoaQi8PPxE4JG8TUmtw5Zf/p2p-circuit"), Transport: "relay", Relayed: true, Opened: now.Add(-time.Hour), Source: p2p.SourceHelper},
			{Remote: ma.StringCast("/ip4/192.168.1.5/udp/4242/quic-v1"), Transport: "QUIC/IPv4", Inbound: true, Opened: now.Add(-90 * time.Second), Source: p2p.SourceHolePunch},
		},
		Latency:   42 * time.Millisecond,
		HolePunch: p2p.HolePunch{At: now.Add(-90 * time.Second), OK: true},
	}
	f := fields(Details(d, now))
	require.Equal(t, "Connected directly", f["Status"], "a direct connection wins over the relayed one")
	require.Equal(t, "hole punching (upgraded from the relay)", f["Found via"])
	require.Equal(t, "relay to /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWHxukoUg4QZmSWciogRpYmRaoaQi8PPxE4JG8TUmtw5Zf/p2p-circuit, you dialled, open 1h0m0s", f["Connection 1"])
	require.Equal(t, "QUIC/IPv4 to /ip4/192.168.1.5/udp/4242/quic-v1, they dialled, open 1m30s", f["Connection 2"])
	require.Equal(t, "42 ms", f["Latency"])
	require.Equal(t, "succeeded at 11:58:30", f["Hole punch"])
	require.Equal(t, "/ip4/192.168.1.5/tcp/4242", f["Saved addresses"])
	for _, offlineOnly := range []string{"Last seen", "Next attempt"} {
		require.NotContains(t, f, offlineOnly)
	}
}

func TestDetailsOffline(t *testing.T) {
	d := core.ContactDetails{
		LastSeen:      now.Add(-12 * time.Minute),
		LastProblem:   p2p.Explain(errors.New("connect: connection refused")),
		LastProblemAt: now.Add(-time.Minute),
		NextAttempt:   now.Add(80 * time.Second),
	}
	f := fields(Details(d, now))
	require.Equal(t, "Offline", f["Status"])
	require.Equal(t, "11:48:00 (12m0s ago)", f["Last seen"])
	require.Contains(t, f["Last problem"], "refused the connection")
	require.Contains(t, f["Last problem"], "(at 11:59:00)")
	require.Equal(t, "in 1m20s (at 12:01:20)", f["Next attempt"])
	require.Contains(t, f["Saved addresses"], "none")
	require.NotContains(t, f, "Found via")
	require.NotContains(t, f, "Latency")

	d = core.ContactDetails{NextAttempt: now}
	f = fields(Details(d, now))
	require.Equal(t, "not this session", f["Last seen"])
	require.Equal(t, "now", f["Next attempt"])
	require.Equal(t, "none scheduled", fields(Details(core.ContactDetails{}, now))["Next attempt"])
}

func TestStepLinesAndReport(t *testing.T) {
	require.Equal(t, "… Helper", StepLine(p2p.Step{Title: "Helper", Status: p2p.StepRunning}))
	require.Equal(t, "✗ Direct connection: No answer.", StepLine(p2p.Step{Title: "Direct connection", Status: p2p.StepFail, Detail: "No answer."}))

	res := p2p.CheckResult{
		Steps: []p2p.Step{
			{Title: "Helper", Status: p2p.StepOK, Detail: "Connected to the helper."},
			{Title: "Your reachability", Status: p2p.StepInfo, Detail: "Behind a NAT."},
			{Title: "Your relay slot", Status: p2p.StepWarn, Detail: "None yet."},
			{Title: "Hole punch", Status: p2p.StepSkipped, Detail: "Not needed."},
		},
		Verdict: "Connected directly.",
	}
	require.Equal(t, `Connection check: Ben (2026-10-25 12:00:00)
✓ Helper: Connected to the helper.
• Your reachability: Behind a NAT.
! Your relay slot: None yet.
– Hole punch: Not needed.
Verdict: Connected directly.
`, Report("Ben", now, res))
}
