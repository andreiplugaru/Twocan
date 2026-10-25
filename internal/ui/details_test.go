package ui

import (
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"

	"twocan/internal/core"
	"twocan/internal/p2p"
)

func inline(f func()) { f() }

// newTestDetails builds the details dialog content for ann, running the
// API calls synchronously.
func newTestDetails(t *testing.T, f *fakeAPI) *detailsView {
	t.Helper()
	test.NewTempApp(t)
	dv := newDetailsView(t.Context(), f, ann, "Ann", inline, inline)
	w := test.NewWindow(dv.content)
	t.Cleanup(w.Close)
	w.Resize(fyne.NewSize(detailsWidth, detailsHeight))
	return dv
}

func TestDetailsButtonOpensSelectedContact(t *testing.T) {
	v := newTestView(t, standardFake())
	var opened []string
	v.showDetails = func(id peer.ID, name string) { opened = append(opened, string(id)+"/"+name) }

	require.False(t, v.detailsBtn.Visible(), "no contact selected")
	v.selectContact(ann)
	require.True(t, v.detailsBtn.Visible())
	test.Tap(v.detailsBtn)
	require.Equal(t, []string{"ann/Ann"}, opened)
}

func TestDetailsShowFields(t *testing.T) {
	f := standardFake()
	f.contact(ann).Addrs = []ma.Multiaddr{ma.StringCast("/ip4/192.168.1.5/tcp/4242")}
	f.details = core.ContactDetails{
		Conns: []p2p.Conn{{
			Remote: ma.StringCast("/ip4/192.168.1.5/tcp/4242"), Transport: "TCP/IPv4",
			Opened: time.Now(), Source: p2p.SourceMDNS,
		}},
		Latency: 7 * time.Millisecond,
	}
	dv := newTestDetails(t, f)
	texts := labelTexts(dv.fields)
	for _, want := range []string{
		"Status", "Connected directly",
		"Found via", "your local network (mDNS)",
		"Latency", "7 ms",
		"Saved addresses", "/ip4/192.168.1.5/tcp/4242",
	} {
		require.Contains(t, texts, want)
	}
}

func TestDetailsRunCheck(t *testing.T) {
	f := standardFake()
	f.check = p2p.CheckResult{
		Steps: []p2p.Step{
			{Title: "Helper", Status: p2p.StepOK, Detail: "Connected to the helper."},
			{Title: "Direct connection", Status: p2p.StepFail, Detail: "No answer."},
		},
		Verdict: "Couldn't connect.",
	}
	dv := newTestDetails(t, f)
	require.True(t, dv.copy.Disabled(), "nothing to copy before a check")

	test.Tap(dv.check)
	steps := findAll[*widget.Label](dv.steps)
	require.Len(t, steps, 2)
	require.Equal(t, "✓ Helper: Connected to the helper.", steps[0].Text)
	require.Equal(t, widget.SuccessImportance, steps[0].Importance)
	require.Equal(t, "✗ Direct connection: No answer.", steps[1].Text)
	require.Equal(t, widget.DangerImportance, steps[1].Importance)
	require.Equal(t, "Couldn't connect.", dv.verdict.Text)
	require.False(t, dv.check.Disabled())

	require.False(t, dv.copy.Disabled())
	test.Tap(dv.copy)
	copied := fyne.CurrentApp().Clipboard().Content()
	require.Contains(t, copied, "Connection check: Ann (")
	require.Contains(t, copied, "✗ Direct connection: No answer.\nVerdict: Couldn't connect.\n")

	// A second run starts from a clean list.
	test.Tap(dv.check)
	require.Len(t, findAll[*widget.Label](dv.steps), 2)
}

func TestDetailsCheckError(t *testing.T) {
	f := standardFake()
	f.checkErr = errors.New("boom")
	dv := newTestDetails(t, f)
	test.Tap(dv.check)
	require.Equal(t, "boom", dv.verdict.Text)
	require.True(t, dv.copy.Disabled())
}

// Refreshing with the same fields keeps the labels, so selected text stays.
func TestDetailsRefreshKeepsLabels(t *testing.T) {
	f := standardFake()
	dv := newTestDetails(t, f)
	before := append([]fyne.CanvasObject(nil), dv.fields.Objects...)
	dv.refresh()
	require.Equal(t, before, dv.fields.Objects)

	f.details.LastProblem = "No answer."
	dv.refresh()
	require.NotEqual(t, len(before), len(dv.fields.Objects), "a new field rebuilds the list")
	require.Contains(t, labelTexts(dv.fields), "No answer. (at 00:00:00)")
}
