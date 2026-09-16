package einoadapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/imbrooklyn/kupilot/internal/agent"
)

// nativeOllamaRequest restores only the bound catalog schema slots and explicit
// zero omitted by the pinned SDK. Other bytes remain Eino-owned (ADR-0059).
func nativeOllamaRequest(request *http.Request, temperature float64, maximum int) (*http.Request, error) {
	defer request.Body.Close()
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, int64(maximum)+1))
	if err != nil {
		clear(payload)
		return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
	}
	accepted := false
	defer func() {
		if !accepted {
			clear(payload)
		}
	}()
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	if len(payload) > maximum {
		return nil, errModelRequestLimitReached
	}
	if int64(len(payload)) != request.ContentLength {
		return nil, errTransportRequestInvalid
	}
	optionsStart, optionsEnd, err := nativeJSONMember(payload, "options")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
	}
	if optionsStart < 0 {
		return nil, errTransportRequestInvalid
	}
	options := payload[optionsStart:optionsEnd]
	start, end, err := nativeJSONMember(options, "temperature")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
	}
	if start >= 0 {
		var actual float32
		value := options[start:end]
		if err := json.Unmarshal(value, &actual); err != nil {
			return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
		}
		if bytes.Equal(value, []byte("null")) || actual != float32(temperature) || actual == 0 && temperature != 0 {
			return nil, errTransportRequestInvalid
		}
	} else {
		if temperature != 0 {
			return nil, errTransportRequestInvalid
		}
		member := `"temperature":0`
		if len(bytes.TrimSpace(options[1:len(options)-1])) > 0 {
			member += ","
		}
		if len(payload)+len(member) > maximum {
			return nil, errModelRequestLimitReached
		}
		corrected := make([]byte, 0, len(payload)+len(member))
		corrected = append(corrected, payload[:optionsStart+1]...)
		corrected = append(corrected, member...)
		corrected = append(corrected, payload[optionsStart+1:]...)
		clear(payload)
		payload = corrected
	}
	catalog, _ := request.Context().Value(nativeCatalogContextKey{}).([]agent.ToolSpecification)
	schemaBytes, err := nativeToolSchemaBytes(payload, catalog, maximum)
	if err != nil {
		return nil, err
	}
	if schemaBytes != nil {
		clear(payload)
		payload = schemaBytes
	}
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	corrected := request.Clone(request.Context())
	// RoundTrip may return before its writer closes the body. Keep the bytes
	// valid for that owner and GetBody; do not clear them when RoundTrip returns.
	corrected.Body = io.NopCloser(bytes.NewReader(payload))
	corrected.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(payload)), nil
	}
	corrected.ContentLength = int64(len(payload))
	corrected.Header.Del("Content-Length")
	accepted = true
	return corrected, nil
}

// nativeJSONMember locates a unique member without reserializing the object.
// Only fixed request metadata uses it; no key is selected from model output.
func nativeJSONMember(payload []byte, name string) (int, int, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil {
		return -1, -1, err
	}
	if opening != json.Delim('{') {
		return -1, -1, errTransportRequestInvalid
	}
	start, end := -1, -1
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return -1, -1, err
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return -1, -1, err
		}
		if key == name {
			if start >= 0 {
				return -1, -1, errTransportRequestInvalid
			}
			end = int(decoder.InputOffset())
			start = end - len(value)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return -1, -1, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return -1, -1, errTransportRequestInvalid
	}
	return start, end, nil
}
