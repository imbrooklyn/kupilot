package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestGetPreviousPodLogsKeepsPreviousSemanticsAndRestartMetadata(t *testing.T) {
	reader := &fakePodLogReader{readFn: func(_ context.Context, request PodLogReadRequest) (PodLogObservation, error) {
		if !request.Previous || request.Container != "setup" {
			t.Fatalf("ReadPodLog() request = %#v", request)
		}
		return PodLogObservation{
			Pod: podLogTarget(), Container: "setup", InitContainer: true, Previous: true,
			Availability: PodLogAvailable, RestartCount: 3,
			LastTerminationReason: ExternalText{Value: "Error"}, Content: boundedPodLogContent(t, "previous instance failed\n"),
		}, nil
	}}
	tool, err := NewGetPreviousPodLogsTool(logDependencies(reader, &sequenceScopeGuard{}, LogPolicyAllowed))
	if err != nil {
		t.Fatalf("NewGetPreviousPodLogsTool() error = %v", err)
	}
	call := boundLogCall(t, testRunInput(t, 0), domain.ToolNameGetPreviousPodLogs,
		`{"container":"setup","pod_name":"sample-pod","purpose":"Inspect the previous init container logs."}`)
	result := tool.Execute(context.Background(), call)
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 1 ||
		!strings.Contains(result.DataJSON, `"instance":"previous"`) || !strings.Contains(result.DataJSON, `"init_container":true`) ||
		!strings.Contains(result.DataJSON, `"restart_count":3`) || !strings.Contains(result.DataJSON, `"last_termination_reason":"Error"`) {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
}

func TestGetPreviousPodLogsDoesNotFallBackWhenNoPreviousInstanceExists(t *testing.T) {
	reader := &fakePodLogReader{readFn: func(_ context.Context, request PodLogReadRequest) (PodLogObservation, error) {
		if !request.Previous {
			t.Fatal("previous Tool attempted a current log read")
		}
		return PodLogObservation{
			Pod: podLogTarget(), Container: "app", Previous: true,
			Availability: PodLogNoPreviousInstance,
		}, nil
	}}
	tool, _ := NewGetPreviousPodLogsTool(logDependencies(reader, &sequenceScopeGuard{}, LogPolicyAllowed))
	result := tool.Execute(context.Background(), boundLogCall(t, testRunInput(t, 0), domain.ToolNameGetPreviousPodLogs,
		`{"container":"app","pod_name":"sample-pod","purpose":"Inspect previous application logs."}`))
	if result.Validate() != nil || result.Status != domain.ToolResultStatusSuccess || len(result.Evidence) != 0 ||
		!strings.Contains(result.DataJSON, `"availability":"no_previous_instance"`) ||
		len(result.Warnings) != 1 || result.Warnings[0].Code != "no_previous_instance" {
		t.Fatalf("Execute() result = %#v, validation = %v", result, result.Validate())
	}
}

func TestCurrentAndPreviousLogsExposeSeparateFixedSchemas(t *testing.T) {
	var current, previous *domain.ModelToolSpecification
	for _, specification := range agent.ToolSpecifications() {
		specification := specification
		switch specification.Name {
		case domain.ToolNameGetPodLogs:
			current = &specification
		case domain.ToolNameGetPreviousPodLogs:
			previous = &specification
		}
	}
	if current == nil || previous == nil || current.Name == previous.Name || current.Description == previous.Description ||
		current.InputSchemaJSON != previous.InputSchemaJSON || strings.Contains(current.InputSchemaJSON, "previous") ||
		!strings.Contains(current.InputSchemaJSON, "namespace") || strings.Contains(current.InputSchemaJSON, "limit_bytes") ||
		strings.Contains(current.InputSchemaJSON, "follow") {
		t.Fatalf("current/previous specifications = %#v / %#v", current, previous)
	}
}

func TestPodLogBindingDeniesScopeSubresourceAndExpandedLimitsBeforeReader(t *testing.T) {
	reader := &fakePodLogReader{}
	for _, arguments := range []string{
		`{"follow":true,"pod_name":"sample-pod","purpose":"Inspect logs."}`,
		`{"limit_bytes":999999,"pod_name":"sample-pod","purpose":"Inspect logs."}`,
		`{"namespace":"other","pod_name":"sample-pod","purpose":"Inspect logs."}`,
		`{"pod_name":"sample-pod","previous":true,"purpose":"Inspect logs."}`,
		`{"pod_name":"sample-pod","purpose":"Inspect logs.","tail_lines":201}`,
	} {
		_, err := agent.BindToolCall(testRunInput(t, 0), testInvocationID, domain.ModelToolCall{
			ID: "call-log-denied", Name: domain.ToolNameGetPodLogs, ArgumentsJSON: arguments,
		})
		if err == nil || reader.count() != 0 {
			t.Fatalf("BindToolCall(%s) error/reads = %v/%d", arguments, err, reader.count())
		}
	}
}
