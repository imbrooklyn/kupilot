package approval

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// CanonicalOperation returns the single project-owned ActionEnvelope
// representation. Approval state, nonce, and reviewer text are never part of
// executable authority.
func CanonicalOperation(request domain.ApprovalRequest) ([]byte, error) {
	canonical, err := domain.CanonicalAction(request.ActionEnvelope())
	if err != nil {
		return nil, domain.NewApprovalError(domain.ApprovalErrorCodeInvalidRequest)
	}
	return canonical, nil
}

// OperationDigest hashes the exact canonical ActionEnvelope representation.
func OperationDigest(request domain.ApprovalRequest) (domain.ApprovalDigest, error) {
	canonical, err := CanonicalOperation(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return domain.ActionDigest(hex.EncodeToString(digest[:])), nil
}
