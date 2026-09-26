package monty

import (
	"encoding/binary"
	"fmt"
	"io"

	pb "github.com/whale-net/everything/libs/go/monty/protos"
	"google.golang.org/protobuf/proto"
)

// MaxFrameLen is the largest single protocol frame this client will send or
// accept, matching Monty's own cap. A sandboxed worker is untrusted: a frame
// claiming more than this is refused rather than allocated.
const MaxFrameLen = 256 << 20

// stdio framing is a 4-byte little-endian unsigned length prefix followed by
// the protobuf body. The WebSocket transport carries the same body as one
// binary message with no prefix, so only this file knows the difference.

// writeFrame writes one length-prefixed protobuf message.
func writeFrame(w io.Writer, m proto.Message) error {
	body, err := proto.Marshal(m)
	if err != nil {
		return fmt.Errorf("monty: marshal request: %w", err)
	}
	if len(body) > MaxFrameLen {
		return fmt.Errorf("monty: request frame is %d bytes, over the %d byte cap", len(body), MaxFrameLen)
	}
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], uint32(len(body)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// frameReader turns a byte stream back into protobuf messages. It holds no
// state between calls, so a Conn can only stay cancel-safe by reading on a
// dedicated goroutine -- a read cancelled mid-frame would otherwise discard
// the bytes already consumed.
type frameReader struct {
	r io.Reader
}

// next returns the next frame body, or io.EOF at a frame boundary. A stream
// that ends part-way through a frame is a truncation, not a clean EOF, and
// the two mean different things to a caller: the first is a closed
// connection, the second a worker that died holding a half-written message.
func (f *frameReader) next() ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(f.r, prefix[:]); err != nil {
		if err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("monty: frame header truncated: %w", err)
		}
		return nil, err
	}
	n := binary.LittleEndian.Uint32(prefix[:])
	if n > MaxFrameLen {
		return nil, fmt.Errorf("monty: frame is %d bytes, over the %d byte cap", n, MaxFrameLen)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(f.r, body); err != nil {
		if err == io.EOF {
			// io.ReadFull reports a bare EOF when the stream ends without
			// delivering the declared bytes. Normalising it keeps a caller
			// filtering on io.EOF from reading corruption as a clean close.
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("monty: frame body truncated: %w", err)
	}
	return body, nil
}

// decodeEvent unmarshals one child event.
func decodeEvent(body []byte) (*pb.ChildEvent, error) {
	var ev pb.ChildEvent
	if err := proto.Unmarshal(body, &ev); err != nil {
		return nil, fmt.Errorf("%w: undecodable event: %v", ErrProtocol, err)
	}
	if ev.Kind == nil {
		return nil, fmt.Errorf("%w: event carried no kind", ErrProtocol)
	}
	return &ev, nil
}
