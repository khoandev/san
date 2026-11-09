package setting

import (
	"encoding/json"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestAutoTierDefaultsOn(t *testing.T) {
	if !New(nil).AutoTier() {
		t.Fatal("auto tier must default on")
	}
	if !New(&Data{}).AutoTier() {
		t.Fatal("an absent agents block must keep auto tier on")
	}
	if !New(&Data{Agents: AgentsSettings{AutoTier: boolPtr(true)}}).AutoTier() {
		t.Fatal("an explicit true must stay on")
	}
	if New(&Data{Agents: AgentsSettings{AutoTier: boolPtr(false)}}).AutoTier() {
		t.Fatal("an explicit false must turn auto tier off")
	}
}

// TestAutoTierSurvivesMergeAndClone covers the two places a pointer field is
// lost silently: the layered merge (a project file that says nothing must not
// switch the user's opt-out back on) and Clone (the settings service hands every
// reader its own copy).
func TestAutoTierSurvivesMergeAndClone(t *testing.T) {
	base := &Data{Agents: AgentsSettings{AutoTier: boolPtr(false)}}

	merged := mergeSettings(base, &Data{})
	if merged.Agents.AutoTierOn() {
		t.Fatal("a silent project layer turned auto tier back on")
	}
	merged.Agents.AutoTier = boolPtr(true)
	if base.Agents.AutoTierOn() {
		t.Fatal("merge shared the pointer instead of coalescing it")
	}

	off := mergeSettings(&Data{}, base)
	clone := off.Clone()
	if clone.Agents.AutoTierOn() {
		t.Fatal("clone lost the opt-out")
	}
	*off.Agents.AutoTier = true
	if clone.Agents.AutoTierOn() {
		t.Fatal("clone shared the pointer with its source")
	}
}

// TestAutoTierJSONShape pins the file format the setting is documented as:
// {"agents":{"autoTier":false}}.
func TestAutoTierJSONShape(t *testing.T) {
	var data Data
	if err := json.Unmarshal([]byte(`{"agents":{"autoTier":false}}`), &data); err != nil {
		t.Fatal(err)
	}
	if data.Agents.AutoTierOn() {
		t.Fatal("agents.autoTier=false did not survive decoding")
	}
	encoded, err := json.Marshal(Data{Agents: AgentsSettings{AutoTier: boolPtr(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"agents":{"autoTier":false}}` {
		t.Fatalf("encoded settings = %s", encoded)
	}
}
