package bedrock

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"sort"
)

// Bedrock streams with the AWS event-stream framing rather than SSE, so the
// body is binary and has to be decoded frame by frame.
//
// A frame is:
//
//	 total length   uint32
//	 headers length uint32
//	 prelude CRC    uint32   (over the two lengths above)
//	 headers        headersLength bytes
//	 payload        totalLength - headersLength - 16 bytes
//	 message CRC    uint32   (over everything before it)
//
// A header is a one-byte name length, the name, a one-byte type, then a value
// whose encoding depends on the type. Only the string type matters here — the
// two headers that carry meaning, :message-type and :event-type, are strings —
// but the rest still have to be skipped by exactly the right width or the
// reader loses frame alignment.

const (
	preludeLength    = 12 // two lengths plus the prelude CRC
	messageCRCLength = 4
	// maxFrameLength guards against a corrupt length field turning into a
	// gigabyte allocation.
	maxFrameLength = 24 << 20
)

// eventMessage is one decoded frame.
type eventMessage struct {
	// Headers holds the string-valued headers, which is all Bedrock uses.
	Headers map[string]string
	Payload []byte
}

// messageType returns the :message-type header, which is "event" for content
// and "exception" for a mid-stream failure.
func (m eventMessage) messageType() string { return m.Headers[":message-type"] }

// eventType returns the :event-type header, which names the Converse event.
func (m eventMessage) eventType() string { return m.Headers[":event-type"] }

// exceptionType returns the :exception-type header, set on error frames.
func (m eventMessage) exceptionType() string { return m.Headers[":exception-type"] }

// eventStreamReader decodes frames from a response body.
type eventStreamReader struct {
	r io.Reader
	// prelude is reused across frames to avoid an allocation per event.
	prelude [preludeLength]byte
}

// newEventStreamReader wraps a body.
func newEventStreamReader(r io.Reader) *eventStreamReader {
	return &eventStreamReader{r: r}
}

// next reads one frame. It returns io.EOF at the end of the stream.
func (e *eventStreamReader) next() (eventMessage, error) {
	if _, err := io.ReadFull(e.r, e.prelude[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			// A truncated prelude means the connection dropped mid-frame,
			// which is a real error rather than a clean end of stream.
			return eventMessage{}, fmt.Errorf("bedrock: truncated event stream: %w", err)
		}
		return eventMessage{}, err
	}

	totalLength := binary.BigEndian.Uint32(e.prelude[0:4])
	headersLength := binary.BigEndian.Uint32(e.prelude[4:8])
	preludeCRC := binary.BigEndian.Uint32(e.prelude[8:12])

	if crc32.ChecksumIEEE(e.prelude[0:8]) != preludeCRC {
		return eventMessage{}, errors.New("bedrock: event stream prelude checksum mismatch")
	}
	if totalLength < preludeLength+messageCRCLength || totalLength > maxFrameLength {
		return eventMessage{}, fmt.Errorf("bedrock: implausible event frame length %d", totalLength)
	}
	if uint64(headersLength)+preludeLength+messageCRCLength > uint64(totalLength) {
		return eventMessage{}, fmt.Errorf("bedrock: header length %d overruns the frame", headersLength)
	}

	// Everything after the prelude, including the trailing CRC.
	rest := make([]byte, totalLength-preludeLength)
	if _, err := io.ReadFull(e.r, rest); err != nil {
		return eventMessage{}, fmt.Errorf("bedrock: reading event frame: %w", err)
	}

	bodyEnd := len(rest) - messageCRCLength
	messageCRC := binary.BigEndian.Uint32(rest[bodyEnd:])

	// The message CRC covers the prelude as well as the body.
	sum := crc32.ChecksumIEEE(e.prelude[:])
	sum = crc32.Update(sum, crc32.IEEETable, rest[:bodyEnd])
	if sum != messageCRC {
		return eventMessage{}, errors.New("bedrock: event stream message checksum mismatch")
	}

	headers, err := decodeHeaders(rest[:headersLength])
	if err != nil {
		return eventMessage{}, err
	}

	return eventMessage{Headers: headers, Payload: rest[headersLength:bodyEnd]}, nil
}

// Header value type tags, per the event-stream encoding.
const (
	headerTypeBoolTrue  = 0
	headerTypeBoolFalse = 1
	headerTypeByte      = 2
	headerTypeInt16     = 3
	headerTypeInt32     = 4
	headerTypeInt64     = 5
	headerTypeBytes     = 6
	headerTypeString    = 7
	headerTypeTimestamp = 8
	headerTypeUUID      = 9
)

// decodeHeaders reads the header block.
//
// Non-string values are skipped rather than decoded: none of them carry
// meaning for Bedrock, but each has to be stepped over by exactly its own
// width or every subsequent header is garbage.
func decodeHeaders(buf []byte) (map[string]string, error) {
	headers := map[string]string{}

	for len(buf) > 0 {
		nameLength := int(buf[0])
		buf = buf[1:]
		if len(buf) < nameLength+1 {
			return nil, errors.New("bedrock: truncated event stream header")
		}

		name := string(buf[:nameLength])
		valueType := buf[nameLength]
		buf = buf[nameLength+1:]

		switch valueType {
		case headerTypeBoolTrue, headerTypeBoolFalse:
			// The value is the tag itself; nothing follows.

		case headerTypeByte:
			if len(buf) < 1 {
				return nil, errors.New("bedrock: truncated byte header")
			}
			buf = buf[1:]

		case headerTypeInt16:
			if len(buf) < 2 {
				return nil, errors.New("bedrock: truncated int16 header")
			}
			buf = buf[2:]

		case headerTypeInt32:
			if len(buf) < 4 {
				return nil, errors.New("bedrock: truncated int32 header")
			}
			buf = buf[4:]

		case headerTypeInt64, headerTypeTimestamp:
			if len(buf) < 8 {
				return nil, errors.New("bedrock: truncated int64 header")
			}
			buf = buf[8:]

		case headerTypeUUID:
			if len(buf) < 16 {
				return nil, errors.New("bedrock: truncated uuid header")
			}
			buf = buf[16:]

		case headerTypeBytes, headerTypeString:
			if len(buf) < 2 {
				return nil, errors.New("bedrock: truncated header value length")
			}
			valueLength := int(binary.BigEndian.Uint16(buf[:2]))
			buf = buf[2:]
			if len(buf) < valueLength {
				return nil, errors.New("bedrock: truncated header value")
			}
			if valueType == headerTypeString {
				headers[name] = string(buf[:valueLength])
			}
			buf = buf[valueLength:]

		default:
			// An unknown type has no known width, so alignment is already lost.
			return nil, fmt.Errorf("bedrock: unknown event stream header type %d", valueType)
		}
	}

	return headers, nil
}

// encodeEventMessage builds one frame. Tests use it to feed the decoder a
// body that looks like what Bedrock actually sends.
func encodeEventMessage(eventType string, payload []byte) []byte {
	headers := encodeStringHeaders(map[string]string{
		":message-type": "event",
		":event-type":   eventType,
	})

	totalLength := uint32(preludeLength + len(headers) + len(payload) + messageCRCLength)
	prelude := make([]byte, preludeLength)
	binary.BigEndian.PutUint32(prelude[0:4], totalLength)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[0:8]))

	frame := make([]byte, 0, totalLength)
	frame = append(frame, prelude...)
	frame = append(frame, headers...)
	frame = append(frame, payload...)

	sum := crc32.ChecksumIEEE(prelude)
	sum = crc32.Update(sum, crc32.IEEETable, headers)
	sum = crc32.Update(sum, crc32.IEEETable, payload)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, sum)
	return append(frame, crc...)
}

// encodeExceptionMessage builds an exception frame.
func encodeExceptionMessage(exceptionType, message string) []byte {
	headers := encodeStringHeaders(map[string]string{
		":message-type":   "exception",
		":exception-type": exceptionType,
	})
	payload, _ := json.Marshal(map[string]string{"message": message})

	totalLength := uint32(preludeLength + len(headers) + len(payload) + messageCRCLength)
	prelude := make([]byte, preludeLength)
	binary.BigEndian.PutUint32(prelude[0:4], totalLength)
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(prelude[8:12], crc32.ChecksumIEEE(prelude[0:8]))

	frame := make([]byte, 0, totalLength)
	frame = append(frame, prelude...)
	frame = append(frame, headers...)
	frame = append(frame, payload...)

	sum := crc32.ChecksumIEEE(prelude)
	sum = crc32.Update(sum, crc32.IEEETable, headers)
	sum = crc32.Update(sum, crc32.IEEETable, payload)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, sum)
	return append(frame, crc...)
}

// encodeStringHeaders writes a header block of string-valued headers.
func encodeStringHeaders(headers map[string]string) []byte {
	// Stable order so tests comparing raw frames are deterministic.
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	var buf []byte
	for _, name := range names {
		value := headers[name]
		buf = append(buf, byte(len(name)))
		buf = append(buf, name...)
		buf = append(buf, headerTypeString)
		length := make([]byte, 2)
		binary.BigEndian.PutUint16(length, uint16(len(value)))
		buf = append(buf, length...)
		buf = append(buf, value...)
	}
	return buf
}
