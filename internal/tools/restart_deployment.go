package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	// RestartDeploymentProposalName is the isolated v0.2 proposal operation.
	RestartDeploymentProposalName = "restart_deployment"
	// RestartDeploymentProposalVersion fixes the proposal-only model contract.
	RestartDeploymentProposalVersion = "restart-deployment-proposal/v1"

	restartDeploymentProposalSchema        = `{"additionalProperties":false,"properties":{"reason":{"maxLength":512,"minLength":1,"type":"string"}},"required":["reason"],"type":"object"}`
	maxRestartDeploymentProposalInputBytes = 4096
)

var (
	// ErrInvalidRestartDeploymentProposalBridge reports missing dependencies.
	ErrInvalidRestartDeploymentProposalBridge = errors.New("restart Deployment proposal bridge dependencies are invalid")
	// ErrRestartDeploymentProposalDenied reports a strict local proposal denial.
	ErrRestartDeploymentProposalDenied = errors.New("restart Deployment proposal was denied by the fixed runtime policy")
)

// RestartDeploymentProposalSpecification is an isolated v0.2 model-visible
// definition. It is intentionally absent from the v0.1 six-Tool catalog.
type RestartDeploymentProposalSpecification struct {
	Name            string
	Version         string
	Description     string
	InputSchemaJSON string
}

// RestartDeploymentProposalTarget is trusted runtime-bound identity. None of
// these fields are accepted from model arguments.
type RestartDeploymentProposalTarget struct {
	RunID                domain.AgentRunID
	SessionID            domain.SessionID
	Sequence             int64
	Scope                domain.ScopeSnapshot
	DeploymentName       string
	DeploymentUID        string
	TemplateFingerprint  string
	DeploymentGeneration int64
}

// Validate checks the complete bound target using the fixed operation policy.
func (target RestartDeploymentProposalTarget) Validate() error {
	intent := target.intent("Bound proposal validation.")
	if !target.RunID.Valid() || !target.SessionID.Valid() || target.Sequence < 1 || target.Sequence > 4096 ||
		intent.Validate() != nil {
		return ErrRestartDeploymentProposalDenied
	}
	return nil
}

func (target RestartDeploymentProposalTarget) intent(reason string) domain.OperationIntent {
	return domain.OperationIntent{
		Operation:            domain.ApprovalOperationRestartDeployment,
		Scope:                target.Scope,
		DeploymentName:       target.DeploymentName,
		DeploymentUID:        target.DeploymentUID,
		TemplateFingerprint:  target.TemplateFingerprint,
		DeploymentGeneration: target.DeploymentGeneration,
		PolicyVersion:        domain.RestartDeploymentApprovalPolicyVersion,
		ReasonSummary:        reason,
	}
}

// RestartDeploymentProposalSink is implemented by Application. It creates a
// pending request and exposes no executor, patch, YAML, or Kubernetes client.
type RestartDeploymentProposalSink interface {
	SubmitRestartDeploymentProposal(
		context.Context,
		domain.AgentRunID,
		domain.SessionID,
		int64,
		domain.OperationIntent,
	) (domain.ApprovalRequest, error)
}

// RestartDeploymentProposalResult is the only model-bound result. Pending is
// not execution success and the one-time decision nonce is excluded.
type RestartDeploymentProposalResult struct {
	RequestID   domain.ApprovalID     `json:"request_id"`
	State       domain.ApprovalState  `json:"state"`
	Digest      domain.ApprovalDigest `json:"operation_digest"`
	RequestedAt time.Time             `json:"requested_at"`
	ExpiresAt   time.Time             `json:"expires_at"`
	Summary     string                `json:"summary"`
}

// Validate checks the bounded pending-only result shape.
func (result RestartDeploymentProposalResult) Validate() error {
	if !result.RequestID.Valid() || result.State != domain.ApprovalStatePending || !result.Digest.Valid() ||
		result.RequestedAt.IsZero() || result.RequestedAt.Location() != time.UTC ||
		result.RequestedAt.UnixMilli() < 0 || !result.RequestedAt.Equal(time.UnixMilli(result.RequestedAt.UnixMilli()).UTC()) ||
		result.ExpiresAt.Location() != time.UTC ||
		!result.ExpiresAt.Equal(result.RequestedAt.Add(domain.ApprovalExecutionTTL)) ||
		result.Summary != "Approval is pending local user review; no operation was executed." {
		return ErrRestartDeploymentProposalDenied
	}
	return nil
}

// RestartDeploymentProposalBridge validates model arguments and forwards only
// a fixed intent to Application.
type RestartDeploymentProposalBridge struct {
	sink RestartDeploymentProposalSink
	text TextProcessor
}

// NewRestartDeploymentProposalBridge constructs the isolated proposal bridge.
func NewRestartDeploymentProposalBridge(sink RestartDeploymentProposalSink, textProcessor TextProcessor) (*RestartDeploymentProposalBridge, error) {
	if sink == nil || textProcessor == nil {
		return nil, ErrInvalidRestartDeploymentProposalBridge
	}
	return &RestartDeploymentProposalBridge{sink: sink, text: textProcessor}, nil
}

// Specification returns the fixed proposal-only schema.
func (*RestartDeploymentProposalBridge) Specification() RestartDeploymentProposalSpecification {
	return RestartDeploymentProposalSpecification{
		Name:            RestartDeploymentProposalName,
		Version:         RestartDeploymentProposalVersion,
		Description:     "Propose restarting the bound Deployment for explicit local approval; this does not execute the operation.",
		InputSchemaJSON: restartDeploymentProposalSchema,
	}
}

// Propose creates at most one pending request. It never calls an executor.
func (bridge *RestartDeploymentProposalBridge) Propose(
	ctx context.Context,
	target RestartDeploymentProposalTarget,
	argumentsJSON string,
) (RestartDeploymentProposalResult, error) {
	if ctx == nil || bridge == nil || bridge.sink == nil || bridge.text == nil {
		return RestartDeploymentProposalResult{}, ErrInvalidRestartDeploymentProposalBridge
	}
	if err := ctx.Err(); err != nil {
		return RestartDeploymentProposalResult{}, err
	}
	if target.Validate() != nil {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	reason, err := decodeRestartDeploymentProposalArguments(argumentsJSON)
	if err != nil {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	processed, err := bridge.text.Process(reason, domain.MaxApprovalReasonSummaryBytes)
	if err != nil || processed.Value == "" || processed.Truncated || looksLikeWritePayload(processed.Value) {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	intent := target.intent(processed.Value)
	if intent.Validate() != nil {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	request, err := bridge.sink.SubmitRestartDeploymentProposal(ctx, target.RunID, target.SessionID, target.Sequence, intent)
	if err != nil {
		if ctx.Err() != nil {
			return RestartDeploymentProposalResult{}, ctx.Err()
		}
		return RestartDeploymentProposalResult{}, err
	}
	if request.Validate() != nil || request.State != domain.ApprovalStatePending || request.RunID != target.RunID ||
		request.SessionID != target.SessionID || request.Intent != intent {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	digest, err := approval.OperationDigest(request)
	if err != nil || !request.Digest.Equal(digest) {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	result := RestartDeploymentProposalResult{
		RequestID: request.ID, State: request.State, Digest: request.Digest,
		RequestedAt: request.RequestedAt, ExpiresAt: request.ExpiresAt,
		Summary: "Approval is pending local user review; no operation was executed.",
	}
	if result.Validate() != nil {
		return RestartDeploymentProposalResult{}, ErrRestartDeploymentProposalDenied
	}
	return result, nil
}

func decodeRestartDeploymentProposalArguments(value string) (string, error) {
	if value == "" || len(value) > maxRestartDeploymentProposalInputBytes {
		return "", ErrRestartDeploymentProposalDenied
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return "", ErrRestartDeploymentProposalDenied
	}
	seenReason := false
	reason := ""
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || key != "reason" || seenReason {
			return "", ErrRestartDeploymentProposalDenied
		}
		seenReason = true
		if err := decoder.Decode(&reason); err != nil {
			return "", ErrRestartDeploymentProposalDenied
		}
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || !seenReason {
		return "", ErrRestartDeploymentProposalDenied
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", ErrRestartDeploymentProposalDenied
	}
	if reason == "" || len(reason) > domain.MaxApprovalReasonSummaryBytes || strings.TrimSpace(reason) != reason {
		return "", ErrRestartDeploymentProposalDenied
	}
	return reason, nil
}

func looksLikeWritePayload(reason string) bool {
	for _, line := range strings.Split(strings.ToLower(reason), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"apiversion:", "kind:", "metadata:", "spec:", "patch:", "---"} {
			if strings.HasPrefix(line, prefix) {
				return true
			}
		}
	}
	return false
}
