package einoadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func nativeCatalogFixture(catalog []agent.ToolSpecification, complete bool) string {
	var entries []string
	for _, specification := range catalog {
		parameters := `{"type":"object"}`
		if complete {
			parameters = specification.InputSchemaJSON
		}
		entries = append(entries, `{"type":"function","function":{"name":"`+string(specification.Name)+`","description":"`+specification.Description+`","parameters":`+parameters+`}}`)
	}
	return ` {"messages":[{"role":"user","content":"Synthetic parameters stay unchanged."}],"options":{"temperature":0.1},"tools":[ ` + strings.Join(entries, ",\n") + ` ],"stream":true} `
}

func TestNativeCatalogPreservesOtherBytesAndHTTPFraming(t *testing.T) {
	for _, mode := range []agent.RunMode{agent.RunModeOrdinary, agent.RunModePlanOnly} {
		catalog := toolSpecificationsForMode(mode)
		for _, complete := range []bool{false, true} {
			input, want := nativeCatalogFixture(catalog, complete), nativeCatalogFixture(catalog, true)
			ctx := context.WithValue(context.Background(), nativeCatalogContextKey{}, catalog)
			request := nativeGuardedRequest(t, ctx, input, domain.MaxModelRequestBytes)
			body := &nativeReadFixture{reader: strings.NewReader(input)}
			request.Body = body
			corrected, err := nativeOllamaRequest(request, 0.1, domain.MaxModelRequestBytes)
			if err != nil || corrected == nil {
				t.Fatal("valid catalog request was rejected")
			}
			actual, readErr := io.ReadAll(corrected.Body)
			_ = corrected.Body.Close()
			copyBody, copyErr := corrected.GetBody()
			if copyErr != nil {
				t.Fatal("corrected body cannot be replayed locally")
			}
			copied, copiedErr := io.ReadAll(copyBody)
			_ = copyBody.Close()
			if readErr != nil || copiedErr != nil || string(actual) != want || !bytes.Equal(actual, copied) || corrected.ContentLength != int64(len(want)) || !body.closed || request.ContentLength != int64(len(input)) {
				t.Fatal("schema restoration changed unrelated bytes, framing, or body ownership")
			}
		}
	}
}

func TestNativeCatalogEnvelopeDenialsHaveZeroExternalCalls(t *testing.T) {
	catalog := agent.ToolSpecifications()
	valid := nativeCatalogFixture(catalog, false)
	first := catalog[0]
	firstName := `"name":"` + string(first.Name) + `"`
	firstDescription := `"description":"` + first.Description + `"`
	firstParameters := `"parameters":{"type":"object"}`
	for _, test := range []struct {
		name, input string
		unbound     bool
	}{
		{"missing_tools", `{"options":{"temperature":0.1}}`, false},
		{"null_tools", `{"options":{"temperature":0.1},"tools":null}`, false},
		{"object_tools", `{"options":{"temperature":0.1},"tools":{}}`, false},
		{"empty_tools", `{"options":{"temperature":0.1},"tools":[]}`, false},
		{"duplicate_tools", strings.Replace(valid, `"tools":`, `"tools":[],"tools":`, 1), false},
		{"one_missing", nativeCatalogFixture(catalog[1:], false), false},
		{"one_over", nativeCatalogFixture(append(append([]agent.ToolSpecification(nil), catalog...), first), false), false},
		{"out_of_order", nativeCatalogFixture(append([]agent.ToolSpecification{catalog[1], catalog[0]}, catalog[2:]...), false), false},
		{"unbound_tools", valid, true},
		{"unknown_name", strings.Replace(valid, firstName, `"name":"unknown_tool"`, 1), false},
		{"description_changed", strings.Replace(valid, firstDescription, `"description":"Changed."`, 1), false},
		{"type_changed", strings.Replace(valid, `"type":"function"`, `"type":"unknown"`, 1), false},
		{"unknown_tool_field", strings.Replace(valid, `"type":"function"`, `"extra":true,"type":"function"`, 1), false},
		{"unknown_function_field", strings.Replace(valid, firstName, `"extra":true,`+firstName, 1), false},
		{"duplicate_type", strings.Replace(valid, `"type":"function"`, `"type":"function","type":"function"`, 1), false},
		{"duplicate_function", strings.Replace(valid, `"function":`, `"function":{},"function":`, 1), false},
		{"duplicate_name", strings.Replace(valid, firstName, firstName+`,`+firstName, 1), false},
		{"duplicate_description", strings.Replace(valid, firstDescription, firstDescription+`,`+firstDescription, 1), false},
		{"duplicate_parameters", strings.Replace(valid, firstParameters, firstParameters+`,`+firstParameters, 1), false},
		{"missing_parameters", strings.Replace(valid, `,`+firstParameters, ``, 1), false},
		{"null_parameters", strings.Replace(valid, firstParameters, `"parameters":null`, 1), false},
		{"array_parameters", strings.Replace(valid, firstParameters, `"parameters":[]`, 1), false},
		{"string_parameters", strings.Replace(valid, firstParameters, `"parameters":"invalid"`, 1), false},
		{"malformed_parameters", strings.Replace(valid, firstParameters, `"parameters":!`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if !test.unbound {
				ctx = context.WithValue(ctx, nativeCatalogContextKey{}, catalog)
			}
			calls := 0
			configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
			client, _ := newFixtureOllamaClientWithTransport(t, configuration, nil, roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected external call")
			}))
			request := nativeGuardedRequest(t, ctx, test.input, domain.MaxModelRequestBytes)
			body := &nativeReadFixture{reader: strings.NewReader(test.input)}
			request.Body = body
			response, err := client.client.Transport.RoundTrip(request)
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			if response != nil || !errors.Is(err, errTransportRequestInvalid) || calls != 0 || !body.closed {
				t.Fatal("catalog denial failed its exact error, zero-call, or closure contract")
			}
		})
	}
}

func TestNativeCatalogExactLimitAndOneOver(t *testing.T) {
	catalog := agent.ToolSpecifications()
	input, want := nativeCatalogFixture(catalog, false), nativeCatalogFixture(catalog, true)
	for _, maximum := range []int{len(want), len(want) - 1} {
		calls := 0
		configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
		client, _ := newFixtureOllamaClientWithTransport(t, configuration, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			body, err := io.ReadAll(request.Body)
			if err != nil || string(body) != want || request.ContentLength != int64(len(want)) {
				t.Error("exact-limit request changed unrelated bytes or framing")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}))
		ctx := context.WithValue(context.Background(), nativeCatalogContextKey{}, catalog)
		response, err := client.client.Transport.RoundTrip(nativeGuardedRequest(t, ctx, input, maximum))
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if maximum == len(want) {
			if err != nil || response == nil || calls != 1 {
				t.Fatal("exact-limit catalog request did not make exactly one call")
			}
		} else if response != nil || !errors.Is(err, errModelRequestLimitReached) || calls != 0 {
			t.Fatal("one-over catalog request crossed the network boundary")
		}
	}
}

func TestNativeCatalogBoundViewsDelegateAndFreezeCatalog(t *testing.T) {
	for _, mode := range []agent.RunMode{agent.RunModeOrdinary, agent.RunModePlanOnly} {
		catalog := toolSpecificationsForMode(mode)
		infos := make([]*schema.ToolInfo, len(catalog))
		for i, specification := range catalog {
			infos[i], _ = toolInfo(specification)
		}
		configuration := fixtureOllamaConfiguration("http://127.0.0.1:11434", time.Second)
		calls := 0
		client, _ := newFixtureOllamaClientWithTransport(t, configuration, nil, roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if !reflect.DeepEqual(request.Context().Value(nativeCatalogContextKey{}), catalog) {
				t.Error("request lost its bound catalog")
			}
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: io.NopCloser(strings.NewReader(`{"model":"fixture-model","message":{"role":"assistant","content":"Available."},"done":true,"done_reason":"stop"}` + "\n"))}, nil
		}))
		bound, err := client.withTools(infos)
		if err != nil {
			t.Fatal("valid native binding rejected")
		}
		bound, err = bound.WithTools(infos)
		if err != nil {
			t.Fatal("valid native rebinding rejected")
		}
		message, failure := streamFixture(client, bound, context.Background())
		if failure != nil || message == nil || message.Content != "Available." || calls != 1 {
			t.Fatal("bound native stream did not delegate exactly once")
		}
		if _, err := bound.WithTools(nil); !errors.Is(err, agent.ErrToolPolicyDenied) || calls != 1 {
			t.Fatal("invalid rebinding was accepted")
		}
	}
}

func TestNativeCatalogToolFreeAndMalformedHelpers(t *testing.T) {
	for _, payload := range []string{`{}`, `{"tools":[]}`} {
		if body, err := nativeToolSchemaBytes([]byte(payload), nil, 1024); err != nil || body != nil {
			t.Fatal("Tool-free request acquired replacement bytes")
		}
	}
	if _, err := nativeToolSchemaBytes([]byte(`{"tools":!}`), nil, 1024); !errors.Is(err, errTransportRequestInvalid) {
		t.Fatal("malformed request lost its safe error identity")
	}
}

type nativeBindingFailureModel struct {
	einomodel.ToolCallingChatModel
	cause error
}

func (model nativeBindingFailureModel) WithTools(_ []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return nil, model.cause
}

func TestNativeCatalogBindingPreservesCauseAndGenerateDelegates(t *testing.T) {
	cause := errors.New("synthetic native binding failure")
	if bound, err := bindNativeCatalog(nativeBindingFailureModel{cause: cause}, fixtureToolInfos(t)); bound != nil || !errors.Is(err, cause) {
		t.Fatal("native binding failure lost its original cause")
	}
	model := &recordingModel{scripts: []modelScript{func(ctx context.Context, _ recordedModelRequest) ([]*schema.Message, error) {
		if !reflect.DeepEqual(ctx.Value(nativeCatalogContextKey{}), agent.ToolSpecifications()) {
			t.Error("Generate lost the immutable code-owned catalog")
		}
		return []*schema.Message{schema.AssistantMessage("Available.", nil)}, nil
	}}}
	infos := fixtureToolInfos(t)
	bound, err := bindNativeCatalog(model, infos)
	if err != nil {
		t.Fatal("valid native catalog rejected")
	}
	infos[0].Name = "changed_after_binding"
	message, err := bound.Generate(context.Background(), fixtureMessages())
	if err != nil || message == nil || message.Content != "Available." || len(model.Requests()) != 1 {
		t.Fatal("native Generate did not delegate exactly once")
	}
}

type nativeLateCancellation struct {
	context.Context
	checks int
}

func (ctx *nativeLateCancellation) Err() error {
	ctx.checks++
	if ctx.checks == 3 {
		return context.Canceled
	}
	return nil
}

func TestNativeCatalogCancellationBeforeCorrectedBodyAcceptance(t *testing.T) {
	catalog := agent.ToolSpecifications()
	ctx := &nativeLateCancellation{Context: context.WithValue(context.Background(), nativeCatalogContextKey{}, catalog)}
	input := nativeCatalogFixture(catalog, false)
	request := nativeGuardedRequest(t, ctx, input, domain.MaxModelRequestBytes)
	body := &nativeReadFixture{reader: strings.NewReader(input)}
	request.Body = body
	if corrected, err := nativeOllamaRequest(request, 0.1, domain.MaxModelRequestBytes); corrected != nil || !errors.Is(err, context.Canceled) || !body.closed {
		t.Fatal("late cancellation accepted a corrected request or retained its input body")
	}
}
