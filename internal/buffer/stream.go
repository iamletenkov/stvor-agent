package buffer

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/iamletenkov/stvor-agent/internal/atomicfile"
)

const (
	streamFile = "stream.json"
	// seqBlock is how many seq the stream file reserves at once: one write
	// per block, not per record or batch (eMMC wear). A restart skips the
	// rest of the block, so seq still grows.
	seqBlock = 4096
)

// streamState is the stream file: the stream ULID, the last reserved seq and
// the buffer uptime (the age basis of data without a synced clock).
type streamState struct {
	Stream   string `json:"stream"`
	Reserved uint64 `json:"reserved"`
	UptimeMS int64  `json:"uptime_ms"`
}

// loadStream reads the stream file; a missing or damaged file starts a new
// stream (the portal accepts a new stream of the same robot).
func (b *Buffer) loadStream(minNext uint64, minUp int64) error {
	path := filepath.Join(b.cfg.Dir, streamFile)
	var st streamState
	data, _, err := (atomicfile.OS{}).ReadNoFollow(path, 4096)
	if err != nil || json.Unmarshal(data, &st) != nil || len(st.Stream) != ulidLen {
		id, err := newULID(b.cfg.Now(), b.cfg.Rand)
		if err != nil {
			return err
		}
		st = streamState{Stream: id}
	}
	b.stream = st.Stream
	b.nextSeq = max(st.Reserved+1, minNext, 1)
	b.reserved = 0
	b.upBase, b.monoOpen = max(st.UptimeMS, minUp, 0), b.cfg.MonoMS()
	return b.reserve()
}

func (b *Buffer) reserve() error {
	reserved := b.nextSeq + seqBlock - 1
	if err := b.writeStream(reserved); err != nil {
		return err
	}
	b.reserved = reserved
	return nil
}

// saveStream stores the uptime on a clean stop.
func (b *Buffer) saveStream() error { return b.writeStream(b.reserved) }

func (b *Buffer) writeStream(reserved uint64) error {
	data, err := json.Marshal(streamState{Stream: b.stream, Reserved: reserved, UptimeMS: b.uptime()})
	if err != nil {
		return err
	}
	if err := b.faultAt("write", streamFile); err != nil {
		return fmt.Errorf("поток буфера: %w", err)
	}
	if err := (atomicfile.OS{}).WriteAtomic(filepath.Join(b.cfg.Dir, streamFile), data, 0o600); err != nil {
		return fmt.Errorf("поток буфера: %w", err)
	}
	return nil
}

func (b *Buffer) allocSeq() (uint64, error) {
	if b.nextSeq > b.reserved {
		if err := b.reserve(); err != nil {
			return 0, err
		}
	}
	s := b.nextSeq
	b.nextSeq++
	return s, nil
}

const (
	ulidLen   = 26
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// newULID is a ULID: 48 bits of Unix ms and 80 random bits, Crockford base32.
func newULID(at time.Time, r io.Reader) (string, error) {
	var b [16]byte
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(max(at.UnixMilli(), 0))) //nolint:gosec // clamped to ≥ 0
	copy(b[:6], ts[2:])
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", fmt.Errorf("ULID: %w", err)
	}
	out := make([]byte, ulidLen)
	for i := range ulidLen {
		lo := 5 * (ulidLen - 1 - i) // bit index of the char's lowest bit
		v := 0
		for k := range 5 {
			bit := lo + k
			if bit < 128 && b[15-bit/8]>>(bit%8)&1 == 1 {
				v |= 1 << k
			}
		}
		out[i] = crockford[v]
	}
	return string(out), nil
}
