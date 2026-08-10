package openaicompat

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

type boundedFuzzReader struct {
	data      []byte
	offset    int
	chunkSize int
}

func (reader *boundedFuzzReader) Read(target []byte) (int, error) {
	if reader.offset == len(reader.data) {
		return 0, io.EOF
	}
	if len(target) > reader.chunkSize {
		target = target[:reader.chunkSize]
	}
	count := copy(target, reader.data[reader.offset:])
	reader.offset += count
	return count, nil
}

func FuzzBoundedSSEBodyIsChunkIndependent(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("data: {\"choices\":[]}\n\n"),
		[]byte("data:{\"choices\":[]}\r\n\r\n"),
		[]byte("data: " + strings.Repeat("x", domain.MaxModelStreamEventBytes+1) + "\n\n"),
		[]byte(strings.Repeat("data: {}\n\n", domain.MaxModelStreamEvents+1)),
		[]byte(strings.Repeat(":", domain.MaxModelStreamBytes+1)),
	} {
		f.Add(seed, uint8(17))
	}

	f.Fuzz(func(t *testing.T, data []byte, encodedChunkSize uint8) {
		if len(data) > domain.MaxModelStreamBytes+1 {
			data = data[:domain.MaxModelStreamBytes+1]
		}
		chunkSize := int(encodedChunkSize%64) + 1
		want, wantErr := readBoundedFuzzSSE(data, 1)
		got, gotErr := readBoundedFuzzSSE(data, chunkSize)
		if !bytes.Equal(got, want) {
			t.Fatalf("bounded SSE output changed at chunk size %d: got %d bytes, want %d", chunkSize, len(got), len(want))
		}
		wantLimited := errors.Is(wantErr, errModelResponseLimitReached)
		gotLimited := errors.Is(gotErr, errModelResponseLimitReached)
		if wantLimited != gotLimited || wantErr != nil && !wantLimited || gotErr != nil && !gotLimited {
			t.Fatalf("bounded SSE error changed at chunk size %d: got %v, want %v", chunkSize, gotErr, wantErr)
		}
		if len(got) > domain.MaxModelStreamBytes {
			t.Fatalf("bounded SSE returned %d bytes, limit %d", len(got), domain.MaxModelStreamBytes)
		}
	})
}

func readBoundedFuzzSSE(data []byte, chunkSize int) ([]byte, error) {
	body := &boundedSSEBody{ReadCloser: io.NopCloser(&boundedFuzzReader{
		data: data, chunkSize: chunkSize,
	})}
	return io.ReadAll(body)
}
