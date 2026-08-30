package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// DefaultStartupNamespace is used only when no effective Namespace is configured.
const DefaultStartupNamespace = "default"

// ErrInvalidScopePreference reports an invalid code-owned preference DTO.
var ErrInvalidScopePreference = errors.New("the Kubernetes Context preference is invalid")

// ScopePreference is the sole durable startup-scope convenience value. It is
// not a live ClusterScope, Session association, client, or authorization.
type ScopePreference struct {
	Context   string
	UpdatedAt time.Time
}

// Validate checks the bounded display name and injected durable timestamp.
func (preference ScopePreference) Validate() error {
	if !domain.ValidContextName(preference.Context) || !validCoordinatorTime(preference.UpdatedAt) {
		return ErrInvalidScopePreference
	}
	return nil
}

// ScopePreferenceStore owns only the last successfully activated Context.
// Missing data is distinct from corrupt or unavailable persistence.
type ScopePreferenceStore interface {
	LoadLastContext(context.Context) (ScopePreference, bool, error)
	SaveLastContext(context.Context, ScopePreference) error
}
