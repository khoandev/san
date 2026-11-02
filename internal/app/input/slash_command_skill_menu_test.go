package input

import (
	"testing"

	"github.com/genai-io/san/internal/skill"
)

// TestSkillCommandInfosFollowsMenuToggle confirms the "/" menu gate: the same
// registry yields entries while skills are listed and none once the listing is
// switched off. Dispatch is untouched — lookupSkill resolves "/<skill>" against
// this registry directly, not through the command menu.
func TestSkillCommandInfosFollowsMenuToggle(t *testing.T) {
	reg := skill.NewRegistryForTest(map[string]*skill.Skill{
		"deploy": {Name: "deploy", Description: "Ship it", ArgumentHint: "<env>", State: skill.StateEnable},
	}, nil, nil)

	infos := skillCommandInfos(reg, true)
	if len(infos) != 1 || infos[0].Name != "deploy" || infos[0].Description != "Ship it <env>" {
		t.Fatalf("listed skills = %#v", infos)
	}
	if infos := skillCommandInfos(reg, false); len(infos) != 0 {
		t.Fatalf("hidden skills still listed in the menu: %#v", infos)
	}
	if infos := skillCommandInfos(nil, true); len(infos) != 0 {
		t.Fatalf("a nil registry must list nothing, got %#v", infos)
	}
}
