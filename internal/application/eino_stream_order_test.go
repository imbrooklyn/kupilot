package application

import (
	"errors"
	"io"
	"strings"
	"testing"
)

const orderContent = `{"choices":[{"index":0,"delta":{"content":"safe"},"finish_reason":null}]}`
const orderFinish = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
const orderUsage = `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`

func TestProviderOrderDecisionMatrix(t *testing.T) {
	cases := []struct {
		name    string
		records []string
		want    error
	}{
		{"content finish done", []string{orderContent, orderFinish, "[DONE]"}, nil},
		{"finish usage done", []string{orderFinish, orderUsage, "[DONE]"}, nil},
		{"finish inline usage", []string{strings.Replace(orderFinish, `}]}`, `}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`, 1), "[DONE]"}, nil},
		{"empty content frame", []string{`{"choices":[{"index":0,"delta":{}}]}`, orderFinish}, nil},
		{"tool finish", []string{strings.Replace(orderFinish, "stop", "tool_calls", 1)}, nil},
		{"length finish", []string{strings.Replace(orderFinish, "stop", "length", 1)}, nil},
		{"duplicate finish", []string{orderFinish, orderFinish}, errProviderFinishDuplicate},
		{"content after finish", []string{orderFinish, orderContent}, errProviderAfterFinish},
		{"event after done", []string{orderFinish, "[DONE]", orderUsage}, errProviderAfterFinish},
		{"done without finish", []string{orderContent, "[DONE]"}, errProviderFinishMissing},
		{"usage before finish", []string{orderUsage}, errProviderUsage},
		{"duplicate usage", []string{orderFinish, orderUsage, orderUsage}, errProviderUsage},
		{"missing choices and usage", []string{`{}`}, errProviderUsage},
		{"null usage", []string{orderFinish, `{"choices":[],"usage":null}`}, errProviderUsage},
		{"usage on content", []string{strings.Replace(orderContent, `}]}`, `}],"usage":{}}`, 1)}, errProviderUsage},
		{"usage wrong type", []string{`{"choices":[],"usage":"invalid"}`}, errProviderUsage},
		{"usage negative", []string{orderFinish, strings.Replace(orderUsage, `"prompt_tokens":2`, `"prompt_tokens":-2`, 1)}, errProviderUsage},
		{"usage negative completion", []string{orderFinish, strings.Replace(orderUsage, `"completion_tokens":3`, `"completion_tokens":-3`, 1)}, errProviderUsage},
		{"usage negative total", []string{orderFinish, strings.Replace(orderUsage, `"total_tokens":5`, `"total_tokens":-5`, 1)}, errProviderUsage},
		{"usage inconsistent sum", []string{orderFinish, strings.Replace(orderUsage, `"total_tokens":5`, `"total_tokens":6`, 1)}, errProviderUsage},
		{"usage prompt exceeds total", []string{orderFinish, strings.Replace(orderUsage, `"prompt_tokens":2`, `"prompt_tokens":6`, 1)}, errProviderUsage},
		{"usage completion exceeds total", []string{orderFinish, strings.Replace(orderUsage, `"completion_tokens":3`, `"completion_tokens":6`, 1)}, errProviderUsage},
		{"usage exact limit", []string{orderFinish, `{"choices":[],"usage":{"prompt_tokens":1000000000,"completion_tokens":0,"total_tokens":1000000000}}`}, nil},
		{"usage one over", []string{orderFinish, `{"choices":[],"usage":{"prompt_tokens":1000000001,"completion_tokens":0,"total_tokens":1000000001}}`}, errProviderUsage},
		{"invalid JSON", []string{`{`}, errMalformedProviderChunk},
		{"multiple choices", []string{`{"choices":[{"index":0},{"index":1}]}`}, errUnsupportedProviderChunk},
		{"missing index", []string{`{"choices":[{}]}`}, errUnsupportedProviderChunk},
		{"wrong index", []string{`{"choices":[{"index":1}]}`}, errUnsupportedProviderChunk},
		{"unknown finish", []string{strings.Replace(orderFinish, "stop", "unknown", 1)}, errProviderStopReason},
	}
	for _, scenario := range cases {
		t.Run(scenario.name, func(t *testing.T) {
			var order sseOrder
			var err error
			for _, record := range scenario.records {
				err = order.accept([]byte(record))
				if err != nil {
					break
				}
			}
			if !errors.Is(err, scenario.want) {
				t.Fatalf("order result = %v; want %v", err, scenario.want)
			}
		})
	}
}

func TestBoundedSSEOrderObserverPreservesBytesAndEOFRejection(t *testing.T) {
	for _, newline := range []string{"", "\n\n"} {
		t.Run(map[bool]string{true: "EOF", false: "newline"}[newline == ""], func(t *testing.T) {
			input := "data: " + orderContent + "\n\ndata: " + orderFinish + newline
			body := &boundedSSEBody{ReadCloser: io.NopCloser(strings.NewReader(input)), validateEvents: true}
			observed, err := io.ReadAll(body)
			if err != nil || string(observed) != input {
				t.Fatalf("valid bytes changed: %v", err)
			}
			invalid := "data: " + orderFinish + "\n\ndata: " + orderFinish + newline
			body = &boundedSSEBody{ReadCloser: io.NopCloser(strings.NewReader(invalid)), validateEvents: true}
			if _, err = io.ReadAll(body); !errors.Is(err, errProviderFinishDuplicate) {
				t.Fatalf("duplicate EOF result = %v", err)
			}
			if _, err = body.Read(make([]byte, 1)); !errors.Is(err, errProviderFinishDuplicate) {
				t.Fatalf("repeated Read = %v", err)
			}
			state := &transportRequestState{}
			if state.responseProtocolFailure() != nil {
				t.Fatal("unused transport has a protocol failure")
			}
		})
	}
}
