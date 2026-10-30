package mobile

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"twocan/internal/core"
	"twocan/internal/event"
)

// events collects a Listener's events.
type events chan map[string]any

func (e events) OnEvent(js string) {
	var m map[string]any
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		panic(err)
	}
	e <- m
}

// waitFor returns the first event of type typ (and satisfying match, if
// given) within 15 s.
func (e events) waitFor(t *testing.T, typ string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case m := <-e:
			if m["type"] == typ && (match == nil || match(m)) {
				return m
			}
		case <-deadline:
			t.Fatalf("no %q event", typ)
		}
	}
}

// startNode starts a node on loopback with no helper.
func startNode(t *testing.T, name string) (*Node, events) {
	t.Helper()
	testOptions = func(o *core.Options) {
		o.ListenAddrs = []string{"/ip4/127.0.0.1/tcp/0"}
		o.Bootstrap = []string{}
	}
	t.Cleanup(func() { testOptions = nil })
	ev := make(events, 100)
	n, err := Start(t.TempDir(), "", "", ev)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, n.Stop()) })
	require.NoError(t, n.SetDisplayName(name))
	return n, ev
}

func contactsOf(t *testing.T, n *Node) []contactJSON {
	t.Helper()
	js, err := n.Contacts()
	require.NoError(t, err)
	var cs []contactJSON
	require.NoError(t, json.Unmarshal([]byte(js), &cs))
	return cs
}

func TestChatEndToEnd(t *testing.T) {
	ana, anaEv := startNode(t, "Ana")
	ben, benEv := startNode(t, "Ben")

	card, err := ben.MyCard()
	require.NoError(t, err)
	require.NoError(t, ana.AddContact(card))
	assert.Equal(t, []contactJSON{{ID: ben.PeerID(), Name: "Ben", State: "pending_outgoing"}},
		withoutLive(contactsOf(t, ana)))

	benEv.waitFor(t, "contacts", func(map[string]any) bool { return len(contactsOf(t, ben)) == 1 })
	req := contactsOf(t, ben)[0]
	assert.Equal(t, ana.PeerID(), req.ID)
	assert.Equal(t, "pending_incoming", req.State)
	require.NoError(t, ben.Accept(ana.PeerID()))
	anaEv.waitFor(t, "contacts", func(map[string]any) bool { return contactsOf(t, ana)[0].State == "accepted" })

	sentJS, err := ana.Send(ben.PeerID(), "hi Ben")
	require.NoError(t, err)
	var sent messageJSON
	require.NoError(t, json.Unmarshal([]byte(sentJS), &sent))
	assert.True(t, sent.Out)
	assert.Equal(t, "hi Ben", sent.Text)

	got := benEv.waitFor(t, "message", nil)["message"].(map[string]any)
	assert.Equal(t, "hi Ben", got["text"])
	assert.Equal(t, ana.PeerID(), got["peer"])
	assert.Equal(t, false, got["out"])
	anaEv.waitFor(t, "delivered", func(m map[string]any) bool { return m["id"] == sent.ID })

	assert.Equal(t, 1, contactsOf(t, ben)[0].Unread)
	require.NoError(t, ben.MarkRead(ana.PeerID()))
	assert.Equal(t, 0, contactsOf(t, ben)[0].Unread)

	histJS, err := ben.History(ana.PeerID())
	require.NoError(t, err)
	var hist []messageJSON
	require.NoError(t, json.Unmarshal([]byte(histJS), &hist))
	require.Len(t, hist, 1)
	assert.Equal(t, "hi Ben", hist[0].Text)

	require.NoError(t, ana.Rename(ben.PeerID(), "Benny"))
	assert.Equal(t, "Benny", contactsOf(t, ana)[0].Name)

	ms, err := ana.Ping(ben.PeerID())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, ms, int64(0))

	detJS, err := ana.Details(ben.PeerID())
	require.NoError(t, err)
	var fields []fieldJSON
	require.NoError(t, json.Unmarshal([]byte(detJS), &fields))
	require.NotEmpty(t, fields)
	assert.Equal(t, fieldJSON{"Status", "Connected directly"}, fields[0])
}

// withoutLive clears the fields that depend on connection timing.
func withoutLive(cs []contactJSON) []contactJSON {
	for i := range cs {
		cs[i].Connected, cs[i].Relayed = false, false
	}
	return cs
}

func TestFriendlyErrors(t *testing.T) {
	n, _ := startNode(t, "Ana")
	err := n.AddContact("hello")
	require.Error(t, err)
	assert.Equal(t, "That is not a Twocan contact card", err.Error())

	card, err := n.MyCard()
	require.NoError(t, err)
	assert.EqualError(t, n.AddContact(card), "You cannot add yourself")

	assert.ErrorIs(t, n.Accept("not-a-peer"), errBadPeer)
}

func TestCardQR(t *testing.T) {
	n, _ := startNode(t, "Ana")
	card, err := n.MyCard()
	require.NoError(t, err)
	png, err := CardQR(card, 256)
	require.NoError(t, err)
	assert.Equal(t, "\x89PNG", string(png[:4]))
}

// checkSteps records a CheckListener's calls.
type checkSteps struct {
	steps   chan string
	verdict chan string
}

func (c checkSteps) OnStep(i int, title, detail, status string) { c.steps <- title + ":" + status }
func (c checkSteps) OnDone(verdict, report string)              { c.verdict <- verdict }

func TestCheckConnectionUnknownContact(t *testing.T) {
	ana, _ := startNode(t, "Ana")
	ben, _ := startNode(t, "Ben")
	c := checkSteps{make(chan string, 100), make(chan string, 1)}
	_, err := ana.CheckConnection(ben.PeerID(), "Ben", c)
	require.NoError(t, err)
	select {
	case v := <-c.verdict:
		assert.NotEmpty(t, v)
	case <-time.After(15 * time.Second):
		t.Fatal("check did not finish")
	}
	assert.Empty(t, c.steps, "a stranger is not checked")
}

func TestEventJSONSkipsUnknown(t *testing.T) {
	_, ok := eventJSON(struct{}{})
	assert.False(t, ok)
}

func TestChatFeatures(t *testing.T) {
	ana, anaEv := startNode(t, "Ana")
	ben, benEv := startNode(t, "Ben")
	card, err := ben.MyCard()
	require.NoError(t, err)
	require.NoError(t, ana.AddContact(card))
	benEv.waitFor(t, "contacts", func(map[string]any) bool { return len(contactsOf(t, ben)) == 1 })
	require.NoError(t, ben.Accept(ana.PeerID()))
	anaEv.waitFor(t, "contacts", func(map[string]any) bool { return contactsOf(t, ana)[0].State == "accepted" })

	js, err := ana.Send(ben.PeerID(), "hike on *saturday*?")
	require.NoError(t, err)
	var q messageJSON
	require.NoError(t, json.Unmarshal([]byte(js), &q))
	benEv.waitFor(t, "message", nil)

	js, err = ben.Reply(ana.PeerID(), q.ID, "yes")
	require.NoError(t, err)
	var r messageJSON
	require.NoError(t, json.Unmarshal([]byte(js), &r))
	require.Equal(t, q.ID, r.ReplyTo)

	require.NoError(t, ben.MarkRead(ana.PeerID()))
	anaEv.waitFor(t, "updated", func(m map[string]any) bool { return m["ids"].([]any)[0] == q.ID })
	require.NoError(t, ana.Edit(ben.PeerID(), q.ID, "hike on sunday?"))
	require.NoError(t, ana.React(ben.PeerID(), r.ID, "👍"))
	benEv.waitFor(t, "updated", func(m map[string]any) bool { return m["ids"].([]any)[0] == r.ID })

	histJS, err := ana.History(ben.PeerID())
	require.NoError(t, err)
	var hist []messageJSON
	require.NoError(t, json.Unmarshal([]byte(histJS), &hist))
	require.Equal(t, "read", hist[0].Status)
	require.Equal(t, "hike on sunday?", hist[0].Text)
	require.NotZero(t, hist[0].EditedAt)
	require.Equal(t, "👍", hist[1].MyReaction)

	found, err := ben.Search("sunday")
	require.NoError(t, err)
	var res []messageJSON
	require.NoError(t, json.Unmarshal([]byte(found), &res))
	require.Len(t, res, 1)
	require.Equal(t, q.ID, res[0].ID)

	require.NoError(t, ana.Delete(ben.PeerID(), q.ID, true))
	require.Equal(t, "You can only edit or delete for everyone your own messages",
		ben.Delete(ana.PeerID(), q.ID, true).Error())
	assert.ErrorIs(t, ana.Edit(ben.PeerID(), "zz", "x"), errBadMessageID)
}

func TestFormat(t *testing.T) {
	assert.JSONEq(t, `[{"text":"a ","bold":false,"italic":false,"url":""},
		{"text":"b","bold":true,"italic":false,"url":""},
		{"text":" ","bold":false,"italic":false,"url":""},
		{"text":"www.x.y","bold":false,"italic":false,"url":"https://www.x.y"}]`, Format("a *b* www.x.y"))
	assert.Equal(t, "[]", Format(""))
}

func TestEventJSONUpdatesAndTyping(t *testing.T) {
	js, ok := eventJSON(event.MessagesUpdated{Peer: "", IDs: [][]byte{{1, 2}}})
	require.True(t, ok)
	assert.JSONEq(t, `{"type":"updated","peer":"","ids":["0102"]}`, js)
	_, ok = eventJSON(event.Typing{})
	require.True(t, ok)
}

func TestNetworkChangedChecksConnections(t *testing.T) {
	ana, _ := startNode(t, "Ana")
	ana.NetworkChanged()
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(ana.log.Path)
		return err == nil && strings.Contains(string(b), `msg="network changed: checking connections"`)
	}, 10*time.Second, 50*time.Millisecond)
}
