package agent

import (
	"strings"
	"testing"
)

func TestAgentSchemaIncludesPredefinedCatalogWithoutSelectors(t *testing.T) {
	catalog := "Available predefined agents:\n\n- reviewer: Reviews changes\n  Use when: Before merging"
	tool := &AgentTool{}
	schema := tool.SchemaWithAgentCatalog(catalog)
	if !strings.Contains(schema.Description, catalog) || !strings.Contains(schema.Description, "Pick the narrowest definition") {
		t.Fatalf("predefined agents are not discoverable: %s", schema.Description)
	}
	// The priority order is what turns a list of definitions into a choice;
	// without it the model picks by name similarity. The last line is the
	// routing opt-out: the model may leave both name and tier unset.
	for _, want := range []string{
		"investigation, including a dependency's upstream docs, → explore",
		"review → reviewer",
		"a bounded change → task",
		"one small edit the brief spells out → quick",
		"Omit name and tier for routine work and san routes it",
	} {
		if !strings.Contains(schema.Description, want) {
			t.Errorf("agent selection guidance lost %q: %s", want, schema.Description)
		}
	}
	if strings.Contains(tool.Schema().Description, "Available predefined agents") {
		t.Fatal("empty registry advertises a catalog")
	}
	if strings.Contains(tool.Schema().Description, "internal investigation") {
		t.Fatal("empty registry points at definitions that may not exist")
	}
	if tool.SchemaWithAgentCatalog(" \n ").Description != tool.Schema().Description {
		t.Fatal("blank catalog changed the schema")
	}
	properties := schema.Definition.(map[string]any)["properties"].(map[string]any)
	for _, removed := range []string{"mode", "model"} {
		if _, exists := properties[removed]; exists {
			t.Fatalf("catalog restored %s selector", removed)
		}
	}
}
