package identity

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "twocan/internal/proto"
)

func testCard(t *testing.T, nAddrs int) Card {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	require.NoError(t, err)
	id, err := peer.IDFromPrivateKey(priv)
	require.NoError(t, err)
	var addrs []ma.Multiaddr
	for i := 0; i < nAddrs; i++ {
		a, err := ma.NewMultiaddr("/ip4/10.0.0." + string(rune('1'+i)) + "/tcp/4001")
		require.NoError(t, err)
		addrs = append(addrs, a)
	}
	return Card{PeerID: id, DisplayName: "Ana 🐦", Addrs: addrs}
}

func TestCardRoundTrip(t *testing.T) {
	c := testCard(t, 5)
	s, err := EncodeCard(c)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(s, CardPrefix))
	require.Equal(t, strings.ToLower(s), s)
	require.NotContains(t, s, "=")
	got, err := DecodeCard(s)
	require.NoError(t, err)
	require.Equal(t, c.PeerID, got.PeerID)
	require.Equal(t, "Ana 🐦", got.DisplayName)
	require.Len(t, got.Addrs, MaxCardAddrs)
	for i, a := range got.Addrs {
		require.True(t, a.Equal(c.Addrs[i]))
	}
}

func TestDecodeCardTolerantInput(t *testing.T) {
	c := testCard(t, 2)
	s, err := EncodeCard(c)
	require.NoError(t, err)
	messy := "  " + strings.ToUpper(s[:20]) + "\n" + s[20:] + " \r\n"
	got, err := DecodeCard(messy)
	require.NoError(t, err)
	want, err := DecodeCard(s)
	require.NoError(t, err)
	require.Equal(t, want, got)
	got, err = DecodeCard(s[:30] + " \t " + s[30:])
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestDecodeCardTruncated(t *testing.T) {
	s, err := EncodeCard(testCard(t, 2))
	require.NoError(t, err)
	_, err = DecodeCard(s[:len(s)-5])
	require.ErrorIs(t, err, ErrDamagedCard)
	require.Contains(t, err.Error(), "card is incomplete or damaged")
}

func enc(t *testing.T, m *pb.Card) string {
	t.Helper()
	b, err := proto.Marshal(m)
	require.NoError(t, err)
	return CardPrefix + strings.ToLower(b32.EncodeToString(b))
}

func TestDecodeCardErrors(t *testing.T) {
	_, err := DecodeCard("hello")
	require.ErrorIs(t, err, ErrNotACard)
	_, err = DecodeCard("")
	require.ErrorIs(t, err, ErrNotACard)

	c := testCard(t, 0)
	_, err = DecodeCard(enc(t, &pb.Card{Version: 2, PeerId: []byte(c.PeerID)}))
	require.ErrorIs(t, err, ErrUnsupportedVersion)
	_, err = DecodeCard(enc(t, &pb.Card{Version: 1}))
	require.ErrorIs(t, err, ErrDamagedCard)
	_, err = DecodeCard(enc(t, &pb.Card{Version: 1, PeerId: []byte("junk")}))
	require.ErrorIs(t, err, ErrDamagedCard)
	_, err = DecodeCard("twocan:!!!")
	require.ErrorIs(t, err, ErrDamagedCard)
}

func TestDecodeCardSkipsBadAddrs(t *testing.T) {
	c := testCard(t, 1)
	got, err := DecodeCard(enc(t, &pb.Card{Version: 1, PeerId: []byte(c.PeerID),
		Addrs: [][]byte{{0xff, 0xff}, c.Addrs[0].Bytes()}}))
	require.NoError(t, err)
	require.Len(t, got.Addrs, 1)
}

func FuzzDecodeCard(f *testing.F) {
	priv, _, _ := crypto.GenerateEd25519Key(nil)
	id, _ := peer.IDFromPrivateKey(priv)
	s, _ := EncodeCard(Card{PeerID: id, DisplayName: "x"})
	f.Add(s)
	f.Add("twocan:")
	f.Add("")
	f.Fuzz(func(t *testing.T, in string) {
		_, _ = DecodeCard(in)
	})
}
