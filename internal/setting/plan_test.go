package setting

import (
	"encoding/json"
	"testing"
)

// TestPlanFastVerifyDefaultsOn pins the opt-out: a plan verifies narrowly
// unless settings.json says otherwise, so a user who never edits the file gets
// the fast ladder rather than the per-step full suite.
func TestPlanFastVerifyDefaultsOn(t *testing.T) {
	if !New(nil).PlanFastVerify() {
		t.Fatal("fast verify must default on")
	}
	if !New(&Data{}).PlanFastVerify() {
		t.Fatal("an absent plan block must keep fast verify on")
	}
	if !New(&Data{Plan: PlanSettings{FastVerify: boolPtr(true)}}).PlanFastVerify() {
		t.Fatal("an explicit true must stay on")
	}
	if New(&Data{Plan: PlanSettings{FastVerify: boolPtr(false)}}).PlanFastVerify() {
		t.Fatal("an explicit false must turn fast verify off")
	}
}

// TestPlanFastVerifySurvivesMergeAndClone checks the two places a pointer
// switch loses its value: a project layer that says nothing, and a clone that
// would otherwise hand the next reader the same pointer.
func TestPlanFastVerifySurvivesMergeAndClone(t *testing.T) {
	base := &Data{Plan: PlanSettings{FastVerify: boolPtr(false)}}

	merged := mergeSettings(base, &Data{})
	if merged.Plan.FastVerifyOn() {
		t.Fatal("a silent project layer turned fast verify back on")
	}
	merged.Plan.FastVerify = boolPtr(true)
	if base.Plan.FastVerifyOn() {
		t.Fatal("merge shared the pointer instead of coalescing it")
	}

	off := mergeSettings(&Data{}, base)
	if off.Plan.FastVerifyOn() {
		t.Fatal("the overlay's opt-out lost to an empty base")
	}
	clone := off.Clone()
	if clone.Plan.FastVerifyOn() {
		t.Fatal("clone lost the opt-out")
	}
	*off.Plan.FastVerify = true
	if clone.Plan.FastVerifyOn() {
		t.Fatal("clone shared the pointer with its source")
	}
}

// TestPlanFastVerifyJSONShape pins the file format the setting is documented
// as: {"plan":{"fastVerify":false}}, and that an unconfigured block stays out
// of the file entirely.
func TestPlanFastVerifyJSONShape(t *testing.T) {
	var data Data
	if err := json.Unmarshal([]byte(`{"plan":{"fastVerify":false}}`), &data); err != nil {
		t.Fatal(err)
	}
	if data.Plan.FastVerifyOn() {
		t.Fatal("plan.fastVerify=false did not survive decoding")
	}

	encoded, err := json.Marshal(Data{Plan: PlanSettings{FastVerify: boolPtr(false)}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"plan":{"fastVerify":false}}` {
		t.Fatalf("encoded settings = %s", encoded)
	}

	encoded, err = json.Marshal(Data{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{}` {
		t.Fatalf("an empty plan block must not be written, got %s", encoded)
	}
}
