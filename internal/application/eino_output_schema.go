package application

import (
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/agenticopenai"
	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/eino-contrib/jsonschema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// These native generation constraints mirror the existing version-one wire
// envelopes. Local decoding, ownership and authority checks remain mandatory.
const nativeCitationSchema = `{"anyOf":[
  {"type":"object","properties":{
    "claim":{"type":"string"},
    "claim_type":{"type":"string","enum":["current_observation"]},
    "evidence_ids":{"type":"array","minItems":1,"items":{"type":"string","pattern":"^e_[0-9a-f]{16}$"}}
  },"required":["claim","claim_type","evidence_ids"],"additionalProperties":false},
  {"type":"object","properties":{
    "claim":{"type":"string"},
    "claim_type":{"type":"string","enum":["inference","recommendation"]},
    "evidence_ids":{"type":"array","items":{"type":"string","pattern":"^e_[0-9a-f]{16}$"}}
  },"required":["claim","claim_type","evidence_ids"],"additionalProperties":false},
  {"type":"object","properties":{
    "claim":{"type":"string"},
    "claim_type":{"type":"string","enum":["uncertainty","unsupported_observation"]},
    "evidence_ids":{"type":"array","maxItems":0,"items":{"type":"string"}}
  },"required":["claim","claim_type","evidence_ids"],"additionalProperties":false}
]}`

const nativeAnswerSchema = `{"type":"object","properties":{
  "answer_markdown":{"type":"string"},
  "evidence_citations":{"type":"array","items":{"$ref":"#/$defs/citation"}},
  "proposed_actions":{"type":"array","maxItems":1,"items":{
    "type":"object","properties":{
      "operation":{"type":"string","enum":["restart_deployment","scale_workload","rollback_deployment","delete_owned_pod","cordon_node","uncordon_node","drain_node","restricted_local_argv","shell"]},
      "reason":{"type":"string"},"risk":{"type":"string"},
      "prerequisites":{"type":"array","items":{"type":"string"}},
      "target":{"type":"object","properties":{
        "api_version":{"type":"string"},"kind":{"type":"string"},"namespace":{"type":"string"},"name":{"type":"string"}
      },"required":["api_version","kind","namespace","name"],"additionalProperties":false},
      "parameters":{"type":["object","null"],"properties":{
        "kind":{"type":"string","enum":["replicas","revision","policy_id"]},"value":{"type":"string"}
      },"required":["kind","value"],"additionalProperties":false}
    },"required":["operation","reason","risk","prerequisites","target","parameters"],"additionalProperties":false
  }},
  "response_schema_version":{"type":"integer","enum":[1]},
  "outcome":{"type":"string","enum":["answer","needs_user_input"]},
  "limitations":{"type":"array","items":{
    "type":"object","properties":{
      "kind":{"type":"string","enum":["absent","forbidden","unsupported","stale","conflicting","truncated","sensitive_output_blocked"]},
      "detail":{"type":"string"},"impact":{"type":"string"}
    },"required":["kind","detail","impact"],"additionalProperties":false
  }},
  "questions":{"type":"array","maxItems":3,"items":{
    "type":"object","properties":{
      "kind":{"type":"string","enum":["choice","free_form"]},"prompt":{"type":"string"},
      "choices":{"type":"array","maxItems":3,"items":{
        "type":"object","properties":{"label":{"type":"string"}},"required":["label"],"additionalProperties":false
      }}
    },"required":["kind","prompt","choices"],"additionalProperties":false
  }}
},"required":["answer_markdown","evidence_citations","proposed_actions","response_schema_version","outcome","limitations","questions"],"additionalProperties":false,
"$defs":{"citation":` + nativeCitationSchema + `}}`

const nativePlanSchema = `{"type":"object","properties":{
  "schema_version":{"type":"integer","enum":[1]},"title":{"type":"string"},
  "steps":{"type":"array","minItems":1,"maxItems":12,"items":{
    "type":"object","properties":{"description":{"type":"string"}},"required":["description"],"additionalProperties":false
  }},
  "limitations":{"type":"array","maxItems":8,"items":{"type":"string"}},
  "evidence_citations":{"type":"array","items":{"$ref":"#/$defs/citation"}}
},"required":["schema_version","title","steps","limitations","evidence_citations"],"additionalProperties":false,
"$defs":{"citation":` + nativeCitationSchema + `}}`

const nativeReviewerSchema = `{"type":"object","properties":{
  "decision":{"type":"string","enum":["approve","deny","escalate_to_user"]},
  "risk":{"type":"string","enum":["safe","review","critical","deny"]},
  "rationale":{"type":"string"}
},"required":["decision","risk","rationale"],"additionalProperties":false}`

func nativeOutputOptions(cfg domain.ModelConfiguration, mode agent.RunMode) ([]einomodel.Option, error) {
	if cfg.ResponseFormat != domain.ModelResponseFormatJSONSchema {
		return nil, nil
	}
	name, wire := "kupilot_answer", nativeAnswerSchema
	if cfg.Role == domain.ModelRoleApprovalReviewer {
		name, wire = "kupilot_review", nativeReviewerSchema
	} else if mode == agent.RunModePlanOnly {
		name, wire = "kupilot_plan", nativePlanSchema
	}
	if cfg.APIProtocol == domain.ModelAPIProtocolResponses {
		var definition map[string]any
		if err := json.Unmarshal([]byte(wire), &definition); err != nil {
			return nil, fmt.Errorf("decode native output schema: %w", err)
		}
		return []einomodel.Option{agenticopenai.WithResponsesText(&responses.ResponseTextConfigParam{Format: responses.ResponseFormatTextConfigUnionParam{OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{Name: name, Schema: definition, Strict: openai.Bool(true)}}})}, nil
	}
	var definition jsonschema.Schema
	if err := json.Unmarshal([]byte(wire), &definition); err != nil {
		return nil, fmt.Errorf("decode native output schema: %w", err)
	}
	fields := map[string]any{"response_format": &einoopenai.ChatCompletionResponseFormat{Type: einoopenai.ChatCompletionResponseFormatTypeJSONSchema, JSONSchema: &einoopenai.ChatCompletionResponseFormatJSONSchema{Name: name, Strict: true, JSONSchema: &definition}}}
	if cfg.Temperature != nil {
		fields["temperature"] = *cfg.Temperature
	}
	return []einomodel.Option{einoopenai.WithExtraFields(fields)}, nil
}
