// Package imgtest has image fixtures shared by tests.
package imgtest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// HugePNGHeader returns the start of a PNG (signature and a valid IHDR
// chunk, no pixel data) that claims to be w×h pixels. Its header decodes;
// decoding the whole image fails for lack of pixel data.
func HugePNGHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8 // bit depth; colour type 0 (gray), default compression, filter, interlace
	chunk := append([]byte("IHDR"), ihdr...)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(ihdr)))
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}
