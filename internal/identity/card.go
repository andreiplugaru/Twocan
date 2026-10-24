package identity

import (
	"encoding/base32"
	"errors"
	"strings"
	"unicode"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"google.golang.org/protobuf/proto"

	pb "twocan/internal/proto"
)

const (
	CardPrefix   = "twocan:"
	CardVersion  = 1
	MaxCardAddrs = 4
)

var (
	ErrNotACard           = errors.New("not a twocan contact card")
	ErrDamagedCard        = errors.New("card is incomplete or damaged")
	ErrUnsupportedVersion = errors.New("contact card version is not supported; update Twocan")
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Card is a shareable contact: peer ID, display name and a few dial addresses.
type Card struct {
	PeerID      peer.ID
	DisplayName string
	Addrs       []ma.Multiaddr
}

// EncodeCard renders c as a "twocan:" string, keeping at most MaxCardAddrs addrs.
func EncodeCard(c Card) (string, error) {
	if c.PeerID == "" {
		return "", errors.New("card has no peer ID")
	}
	addrs := c.Addrs
	if len(addrs) > MaxCardAddrs {
		addrs = addrs[:MaxCardAddrs]
	}
	m := &pb.Card{Version: CardVersion, PeerId: []byte(c.PeerID), DisplayName: c.DisplayName}
	for _, a := range addrs {
		m.Addrs = append(m.Addrs, a.Bytes())
	}
	b, err := proto.Marshal(m)
	if err != nil {
		return "", err
	}
	return CardPrefix + strings.ToLower(b32.EncodeToString(b)), nil
}

// DecodeCard parses a card string, tolerating whitespace anywhere and any case.
func DecodeCard(s string) (Card, error) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if len(s) < len(CardPrefix) || !strings.EqualFold(s[:len(CardPrefix)], CardPrefix) {
		return Card{}, ErrNotACard
	}
	raw, err := b32.DecodeString(strings.ToUpper(s[len(CardPrefix):]))
	if err != nil {
		return Card{}, ErrDamagedCard
	}
	var m pb.Card
	if err := proto.Unmarshal(raw, &m); err != nil {
		return Card{}, ErrDamagedCard
	}
	if m.Version != CardVersion {
		return Card{}, ErrUnsupportedVersion
	}
	if len(m.PeerId) == 0 {
		return Card{}, ErrDamagedCard
	}
	id, err := peer.IDFromBytes(m.PeerId)
	if err != nil {
		return Card{}, ErrDamagedCard
	}
	c := Card{PeerID: id, DisplayName: m.DisplayName}
	for _, ab := range m.Addrs {
		if len(c.Addrs) >= MaxCardAddrs {
			break
		}
		a, err := ma.NewMultiaddrBytes(ab)
		if err != nil {
			continue
		}
		c.Addrs = append(c.Addrs, a)
	}
	return c, nil
}
