package agent

import (
	"strings"
	"testing"
)

func TestAgentSchemaKeepsValidationAndDenialGuidance(t *testing.T) {
	description := agentSchema("").Description
	for _, phrase := range []string{"only task-local context", "the validation plan", "retrying a denied command", "Report a failed tool and its reason"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("description missing %q", phrase)
		}
	}
	properties := agentToolParameters["properties"].(map[string]any)
	if len(properties) != 6 {
		t.Fatalf("Agent parameters = %v, want creation fields only", properties)
	}
	if _, exists := properties["resume_task_id"]; exists {
		t.Fatal("Agent must only create sessions")
	}
	if !strings.Contains(description, "SendMessage to the returned Task ID") {
		t.Fatal("follow-up guidance missing")
	}
}
