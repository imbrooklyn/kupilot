//go:build integration

package application

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	"github.com/cloudwego/eino/schema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// This opt-in compatibility check uses synthetic content and makes one request.
// It does not enable a runtime format, probe, retry, or fallback.
func TestNativeClaimSchemaLive(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_LIVE") != liveModelAuthorizationValue || os.Getenv("KUPILOT_INTEGRATION_MAX_COST_USD") != "3" {
		t.Skip("Requires explicit live model authorization and the three-dollar ceiling.")
	}
	cfg, key, _ := loadLiveModelProfile(t, liveModelTargetPreferred)
	defer key.Destroy()
	if cfg.Model != "gpt-5.6-luna" || cfg.APIProtocol != domain.ModelAPIProtocolResponses {
		t.Skip("Requires the configured Luna Responses profile.")
	}
	transport := newLiveBudgetTransport(cfg.ProviderKind, 1, 4*1024*1024)
	defer transport.base.CloseIdleConnections()
	client, failure := newModelClientForTest(cfg, key, nil, transport)
	if failure != nil {
		t.Fatal("Configured model preflight failed safely.")
	}
	defer client.close()
	var claimSchema map[string]any
	if err := json.Unmarshal([]byte(`{"type":"object","properties":{"claim_type":{"type":"string","enum":["current_observation"]},"evidence_ids":{"type":"array","items":{"type":"string","enum":["e_synthetic"]},"minItems":1,"maxItems":1}},"required":["claim_type","evidence_ids"],"additionalProperties":false}`), &claimSchema); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	state := &transportRequestState{responseMode: transportResponseJSON, requestLimit: domain.MaxModelRequestBytes, responseLimit: domain.MaxModelMessageBytes}
	defer state.closeResponseBody()
	ctx = context.WithValue(ctx, transportRequestStateKey{}, state)
	usage := &conformanceUsage{}
	model := meteredResponsesModel{base: client.responsesModel, usage: usage}
	message, err := model.Generate(ctx, []*schema.AgenticMessage{
		schema.SystemAgenticMessage("Return a JSON object. This is a synthetic schema conformance test with no Tools or cluster access."),
		schema.UserAgenticMessage("Return claim_type current_observation and evidence_ids []."),
	}, agenticopenai.WithResponsesText(&responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: "synthetic_claim", Schema: claimSchema, Strict: openai.Bool(true)}}}))
	estimate := float64(usage.input)*0.20/1e6 + float64(usage.output)*1.20/1e6
	t.Logf("model_calls=%d input_tokens=%d output_tokens=%d estimated_usd=%.6f", transport.calls.Load(), usage.input, usage.output, estimate)
	if err != nil {
		failure := mapModelRequestError(ctx, err, state)
		t.Fatalf("Native strict-schema request failed: code=%s cause=%s http_status=%d; no retry was attempted.", failure.code, failure.cause, failure.httpStatus)
	}
	var claim struct {
		Kind string   `json:"claim_type"`
		IDs  []string `json:"evidence_ids"`
	}
	if json.Unmarshal([]byte(responsesText(message)), &claim) != nil || claim.Kind != "current_observation" || len(claim.IDs) != 1 || claim.IDs[0] != "e_synthetic" {
		t.Fatal("The configured endpoint did not enforce the native non-empty reference constraint.")
	}
}
