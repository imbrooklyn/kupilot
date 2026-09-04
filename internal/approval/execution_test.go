package approval

import (
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRestartExecutionBindsExactApprovedTarget(t *testing.T) {
	intent := testIntent()
	observation := RestartDeploymentObservation{
		Scope: intent.Scope, DeploymentName: intent.Target.Resource.Name, DeploymentUID: intent.Target.Resource.UID,
		TemplateFingerprint: intent.Target.Fingerprint, DeploymentGeneration: intent.Target.Generation,
		ResourceVersion: intent.Target.Resource.ResourceVersion,
	}
	execution, err := NewRestartDeploymentExecution(intent, observation)
	if err != nil || execution.Validate() != nil || execution.Observation() != observation {
		t.Fatalf("NewRestartDeploymentExecution() = %#v/%v", execution, err)
	}
	mutations := []func(*RestartDeploymentObservation){
		func(value *RestartDeploymentObservation) { value.DeploymentUID = "changed" },
		func(value *RestartDeploymentObservation) { value.ResourceVersion = "changed" },
		func(value *RestartDeploymentObservation) { value.TemplateFingerprint = domain.SHA256Hex("changed") },
		func(value *RestartDeploymentObservation) { value.DeploymentGeneration++ },
	}
	for index, mutate := range mutations {
		changed := observation
		mutate(&changed)
		if _, err := NewRestartDeploymentExecution(intent, changed); err == nil {
			t.Fatalf("mutation %d retained execution eligibility", index)
		}
	}
}
