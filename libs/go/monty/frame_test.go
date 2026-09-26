package monty

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/whale-net/everything/libs/go/monty/protos"
	"google.golang.org/protobuf/proto"
)

// TestFrameRoundTrip: writeFrame emits a 4-byte little-endian length followed
// by the bare protobuf body, and frameReader reads exactly that back.
func TestFrameRoundTrip(t *testing.T) {
	req := &pb.ParentRequest{Kind: &pb.ParentRequest_Reset_{Reset_: &pb.Reset{}}}

	var buf bytes.Buffer
	require.NoError(t, writeFrame(&buf, req))

	body, err := proto.Marshal(req)
	require.NoError(t, err)

	framed := buf.Bytes()
	require.Len(t, framed, len(body)+4)
	assert.Equal(t, uint32(len(body)), binary.LittleEndian.Uint32(framed[:4]),
		"the prefix is the body length, little-endian")
	assert.Equal(t, body, framed[4:], "the body is the bare protobuf, with no other prefix")

	got, err := (&frameReader{r: &buf}).next()
	require.NoError(t, err)
	assert.Equal(t, body, got)
}

func TestFrameRoundTripManyMessages(t *testing.T) {
	msgs := []*pb.ChildEvent{
		evtOK(),
		evtPrint(pb.PrintStream_PRINT_STREAM_STDOUT, "hello\n"),
		evtComplete(0, &pb.Arena{NodeCount: 1, Nodes: []*pb.MontyNode{intN(1)}}),
	}

	var buf bytes.Buffer
	for _, m := range msgs {
		require.NoError(t, writeFrame(&buf, m))
	}

	fr := &frameReader{r: &buf}
	for i, want := range msgs {
		body, err := fr.next()
		require.NoError(t, err, "frame %d", i)
		got, err := decodeEvent(body)
		require.NoError(t, err, "frame %d", i)
		assert.True(t, proto.Equal(want, got), "frame %d round-trips", i)
	}
}

func TestFrameEmptyBody(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeFrame(&buf, &pb.Reset{}))

	fr := &frameReader{r: &buf}
	body, err := fr.next()
	require.NoError(t, err)
	assert.Empty(t, body)
}

// TestFrameOverCapRefusedOnWrite: a sandboxed worker is untrusted, so a frame
// claiming more than the cap is refused rather than allocated.
func TestFrameOverCapRefusedOnWrite(t *testing.T) {
	oversized := &pb.ParentRequest{Kind: &pb.ParentRequest_Feed{Feed: &pb.Feed{
		Code: strings.Repeat("x", MaxFrameLen+1),
	}}}

	var buf bytes.Buffer
	err := writeFrame(&buf, oversized)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the")
	assert.Zero(t, buf.Len(), "a refused frame writes nothing at all")
}

func TestFrameOverCapRefusedOnRead(t *testing.T) {
	// The prefix alone is enough to refuse, so nothing huge is ever allocated.
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(MaxFrameLen+1)))

	_, err := (&frameReader{r: &buf}).next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "over the")
}

func TestFrameAtCapIsAccepted(t *testing.T) {
	// The boundary is inclusive: a frame of exactly MaxFrameLen bytes is fine.
	body := bytes.Repeat([]byte{0x00}, MaxFrameLen)
	var buf bytes.Buffer
	require.NoError(t, binary.Write(&buf, binary.LittleEndian, uint32(len(body))))
	buf.Write(body)

	got, err := (&frameReader{r: &buf}).next()
	require.NoError(t, err)
	assert.Len(t, got, MaxFrameLen)
}

// TestFrameCleanEOFIsDistinguishable: a connection that ends at a frame
// boundary is a clean close, which a caller retries; a stream that dies holding
// half a message is a corruption, which it does not.
func TestFrameCleanEOFIsDistinguishable(t *testing.T) {
	_, err := (&frameReader{r: bytes.NewReader(nil)}).next()
	assert.ErrorIs(t, err, io.EOF, "an empty stream is a clean EOF")
	assert.NotErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestFrameTruncated(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stream []byte
		header bool // the failure is in the 4-byte header, not the body
	}{
		{"header: one byte", []byte{0x01}, true},
		{"header: three bytes", []byte{0x01, 0x02, 0x03}, true},
		{
			"body: short of the declared length",
			[]byte{0x08, 0x00, 0x00, 0x00, 0x01, 0x02},
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := (&frameReader{r: bytes.NewReader(tc.stream)}).next()
			require.Error(t, err)
			assert.NotErrorIs(t, err, io.EOF,
				"a truncated frame is a corruption, not a clean close")
			assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
			if tc.header {
				assert.Contains(t, err.Error(), "header truncated")
			} else {
				assert.Contains(t, err.Error(), "body truncated")
			}
		})
	}
}

// TestFrameDeclaredBodyNeverArrived: the frame promised a body and the stream
// ended before delivering any of it. That is a worker that died mid-message,
// so it must not read as the clean EOF of a connection that closed at a frame
// boundary.
func TestFrameDeclaredBodyNeverArrived(t *testing.T) {
	stream := []byte{0x08, 0x00, 0x00, 0x00} // declares 8 bytes, delivers none

	_, err := (&frameReader{r: bytes.NewReader(stream)}).next()
	require.Error(t, err)
	assert.NotErrorIs(t, err, io.EOF,
		"a frame that promised a body is a truncation, not a clean close")
}

// TestFrameTruncatedAfterAWholeFrame: the reader has no state between calls, so
// a good frame is delivered and only the next one is reported truncated.
func TestFrameTruncatedAfterAWholeFrame(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, writeFrame(&buf, &pb.Reset{}))
	buf.Write([]byte{0x08, 0x00}) // a header that stops half way

	fr := &frameReader{r: &buf}
	body, err := fr.next()
	require.NoError(t, err)
	assert.Empty(t, body)

	_, err = fr.next()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "header truncated")
}

func TestDecodeEvent(t *testing.T) {
	arena, roots := mustArena(t, int64(5))
	body, err := proto.Marshal(evtComplete(roots[0], arena))
	require.NoError(t, err)

	ev, err := decodeEvent(body)
	require.NoError(t, err)
	assert.Equal(t, uint32(roots[0]), ev.GetComplete().GetValue())
}

func TestDecodeEventUndecodable(t *testing.T) {
	// Field 1 as a varint, then a length-delimited field claiming more bytes
	// than the message has.
	_, err := decodeEvent([]byte{0x0a, 0x7f, 0x01})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProtocol)
	assert.Contains(t, err.Error(), "undecodable event")
}

// TestDecodeEventWithNoKind: an event with no oneof arm set cannot be
// dispatched, so it is a protocol violation rather than a message to skip.
func TestDecodeEventWithNoKind(t *testing.T) {
	body, err := proto.Marshal(&pb.ChildEvent{})
	require.NoError(t, err)

	_, err = decodeEvent(body)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProtocol)
	assert.Contains(t, err.Error(), "no kind")
}

func TestWriteFrameWriteError(t *testing.T) {
	sentinel := errors.New("pipe is gone")
	err := writeFrame(failingWriter{sentinel}, &pb.Reset{})
	assert.ErrorIs(t, err, sentinel)
}

// failingWriter fails on the first write, so the prefix is where it breaks.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
