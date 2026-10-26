// Package diagtext renders connection details and connection checks as
// text. The CLI and the desktop UI share it so both say the same thing.
package diagtext

import (
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"twocan/internal/core"
	"twocan/internal/p2p"
)

// clock is how times of day are shown.
const clock = "15:04:05"

// Field is one labelled line of connection details.
type Field struct {
	Label, Value string
}

// Details describes d as of now.
func Details(d core.ContactDetails, now time.Time) []Field {
	var out []Field
	add := func(label, format string, args ...any) {
		out = append(out, Field{label, fmt.Sprintf(format, args...)})
	}
	connected := len(d.Conns) > 0
	switch {
	case !connected:
		add("Status", "Offline")
	case mainConn(d.Conns).Relayed:
		add("Status", "Connected through the helper's relay")
	default:
		add("Status", "Connected directly")
	}
	if connected {
		add("Found via", "%s", mainConn(d.Conns).Source)
		for i, c := range d.Conns {
			label := "Connection"
			if len(d.Conns) > 1 {
				label = fmt.Sprintf("Connection %d", i+1)
			}
			who := "you dialled"
			if c.Inbound {
				who = "they dialled"
			}
			add(label, "%s to %s, %s, open %s", c.Transport, c.Remote, who, since(now, c.Opened))
		}
		if d.Latency > 0 {
			add("Latency", "%d ms", d.Latency.Milliseconds())
		} else {
			add("Latency", "not measured yet")
		}
	}
	if hp := d.HolePunch; !hp.At.IsZero() {
		if hp.OK {
			add("Hole punch", "succeeded at %s", hp.At.Format(clock))
		} else {
			add("Hole punch", "failed at %s: %s", hp.At.Format(clock), hp.Err)
		}
	}
	if !connected {
		if d.LastSeen.IsZero() {
			add("Last seen", "not this session")
		} else {
			add("Last seen", "%s (%s ago)", d.LastSeen.Format(clock), since(now, d.LastSeen))
		}
	}
	if d.LastProblem != "" {
		add("Last problem", "%s (at %s)", d.LastProblem, d.LastProblemAt.Format(clock))
	}
	if !connected {
		switch {
		case d.NextAttempt.IsZero():
			add("Next attempt", "none scheduled")
		case !d.NextAttempt.After(now.Add(time.Second)):
			add("Next attempt", "now")
		default:
			add("Next attempt", "in %s (at %s)", d.NextAttempt.Sub(now).Round(time.Second), d.NextAttempt.Format(clock))
		}
	}
	if len(d.Contact.Addrs) == 0 {
		add("Saved addresses", "none: after a restart, Twocan has to find them on your local network or through the helper")
	} else {
		addrs := make([]string, len(d.Contact.Addrs))
		for i, a := range d.Contact.Addrs {
			addrs[i] = a.String()
		}
		add("Saved addresses", "%s", strings.Join(addrs, "\n"))
	}
	return out
}

// mainConn is the connection that matters most: the oldest direct one,
// else the oldest. conns must not be empty.
func mainConn(conns []p2p.Conn) p2p.Conn {
	for _, c := range conns {
		if !c.Relayed {
			return c
		}
	}
	return conns[0]
}

func since(now, t time.Time) string {
	return max(now.Sub(t), 0).Round(time.Second).String()
}

// Reachability says whether others can dial us: "public", "behind NAT" or,
// until AutoNAT has decided, "checking…".
func Reachability(r network.Reachability) string {
	switch r {
	case network.ReachabilityPublic:
		return "public"
	case network.ReachabilityPrivate:
		return "behind NAT"
	}
	return "checking…"
}

// Mark is the one-character symbol for a step status.
func Mark(s p2p.StepStatus) string {
	switch s {
	case p2p.StepOK:
		return "✓"
	case p2p.StepInfo:
		return "•"
	case p2p.StepWarn:
		return "!"
	case p2p.StepFail:
		return "✗"
	case p2p.StepSkipped:
		return "–"
	}
	return "…"
}

// StepLine renders one step, e.g. "✓ Helper: Connected to the helper."
func StepLine(s p2p.Step) string {
	if s.Detail == "" {
		return Mark(s.Status) + " " + s.Title
	}
	return Mark(s.Status) + " " + s.Title + ": " + s.Detail
}

// Report renders a finished check of the contact called name, to copy and
// share. It holds addresses but never message text or keys.
func Report(name string, at time.Time, res p2p.CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Connection check: %s (%s)\n", name, at.Format("2006-01-02 15:04:05"))
	for _, s := range res.Steps {
		b.WriteString(StepLine(s) + "\n")
	}
	b.WriteString("Verdict: " + res.Verdict + "\n")
	return b.String()
}
