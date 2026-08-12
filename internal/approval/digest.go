package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const canonicalOperationFieldCount = 20

// CanonicalOperation returns the versioned, length-prefixed operation bytes.
// The returned slice is independent and contains no nonce or lifecycle state.
func CanonicalOperation(request domain.ApprovalRequest) ([]byte, error) {
	if !request.ID.Valid() || !request.SessionID.Valid() || !request.RunID.Valid() ||
		request.Intent.Validate() != nil || !validCanonicalTime(request.RequestedAt) ||
		!validCanonicalTime(request.ExpiresAt) ||
		!request.ExpiresAt.Equal(request.RequestedAt.Add(domain.ApprovalExecutionTTL)) {
		return nil, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}

	fields := [canonicalOperationFieldCount]canonicalField{
		{name: "request_id", value: string(request.ID)},
		{name: "session_id", value: string(request.SessionID)},
		{name: "run_id", value: string(request.RunID)},
		{name: "operation", value: string(request.Intent.Operation)},
		{name: "operation_schema_version", value: domain.RestartDeploymentOperationSchemaVersion},
		{name: "policy_version", value: request.Intent.PolicyVersion},
		{name: "scope_context", value: request.Intent.Scope.Context},
		{name: "scope_namespace", value: request.Intent.Scope.Namespace},
		{name: "scope_generation", value: strconv.FormatInt(request.Intent.Scope.Generation, 10)},
		{name: "target_api_version", value: domain.RestartDeploymentTargetAPIVersion},
		{name: "target_kind", value: domain.RestartDeploymentTargetKind},
		{name: "target_namespace", value: request.Intent.Scope.Namespace},
		{name: "deployment_name", value: request.Intent.DeploymentName},
		{name: "deployment_uid", value: request.Intent.DeploymentUID},
		{name: "template_fingerprint", value: request.Intent.TemplateFingerprint},
		{name: "deployment_generation", value: strconv.FormatInt(request.Intent.DeploymentGeneration, 10)},
		{name: "reason_summary", value: request.Intent.ReasonSummary},
		{name: "risk_summary", value: domain.RestartDeploymentRiskSummary},
		{name: "requested_at_ms", value: strconv.FormatInt(request.RequestedAt.UnixMilli(), 10)},
		{name: "expires_at_ms", value: strconv.FormatInt(request.ExpiresAt.UnixMilli(), 10)},
	}

	var builder strings.Builder
	builder.Grow(1024)
	builder.WriteString(domain.ApprovalOperationDigestVersion)
	builder.WriteByte('\n')
	for _, field := range fields {
		appendCanonicalField(&builder, field)
	}
	return []byte(builder.String()), nil
}

// OperationDigest hashes the exact canonical operation representation.
func OperationDigest(request domain.ApprovalRequest) (domain.ApprovalDigest, error) {
	canonical, err := CanonicalOperation(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return domain.ApprovalDigest(hex.EncodeToString(digest[:])), nil
}

type canonicalField struct {
	name  string
	value string
}

func appendCanonicalField(builder *strings.Builder, field canonicalField) {
	builder.WriteString(field.name)
	builder.WriteByte('=')
	builder.WriteString(strconv.Itoa(len(field.value)))
	builder.WriteByte(':')
	builder.WriteString(field.value)
	builder.WriteByte('\n')
}

func validCanonicalTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Nanosecond()%int(time.Millisecond) == 0
}
