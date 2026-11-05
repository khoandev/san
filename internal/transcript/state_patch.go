package transcript

import (
	"encoding/json"
	"reflect"

	"github.com/genai-io/san/internal/todo"
)

func (p StatePatch) applyTo(state *State) {
	if p.Title != nil {
		state.Title = *p.Title
	}
	if p.LastPrompt != nil {
		state.LastPrompt = *p.LastPrompt
	}
	if p.Mode != nil {
		state.Mode = *p.Mode
	}
	if p.AutoPilot != nil {
		state.AutoPilot = *p.AutoPilot
	}
	if p.Group != nil {
		state.Group = *p.Group
	}
	if p.Tasks != nil {
		state.Tasks = *p.Tasks
	}
	if p.Plan != nil {
		state.Plan = p.Plan.Clone()
	}
	if p.AdvisorSettings != nil {
		state.AdvisorSettings = *p.AdvisorSettings
	}
}

// changesFrom drops unchanged fields and owns the task payload retained in the cache.
func (p StatePatch) changesFrom(current State) (*StatePatch, error) {
	if p.Title != nil && *p.Title == current.Title {
		p.Title = nil
	}
	if p.LastPrompt != nil && *p.LastPrompt == current.LastPrompt {
		p.LastPrompt = nil
	}
	if p.Mode != nil && *p.Mode == current.Mode {
		p.Mode = nil
	}
	if p.AutoPilot != nil && *p.AutoPilot == current.AutoPilot {
		p.AutoPilot = nil
	}
	if p.Group != nil && *p.Group == current.Group {
		p.Group = nil
	}
	if p.AdvisorSettings != nil && *p.AdvisorSettings == current.AdvisorSettings {
		p.AdvisorSettings = nil
	}
	if p.Tasks != nil {
		// Metadata may contain nested application values; own their durable JSON form.
		data, err := json.Marshal(*p.Tasks)
		if err != nil {
			return nil, err
		}
		tasks := new([]todo.Item)
		if err := json.Unmarshal(data, tasks); err != nil {
			return nil, err
		}
		if len(*tasks) == 0 && len(current.Tasks) == 0 || reflect.DeepEqual(*tasks, current.Tasks) {
			p.Tasks = nil
		} else {
			if *tasks == nil {
				*tasks = []todo.Item{}
			}
			p.Tasks = tasks
		}
	}
	if p.Plan != nil {
		// Own a detached copy so a later UI mutation cannot rewrite a record
		// that was already appended to the log.
		owned := p.Plan.Clone()
		if owned.Empty() && current.Plan.Empty() {
			p.Plan = nil
		} else if reflect.DeepEqual(owned.Plans, current.Plan.Plans) && owned.Mode == current.Plan.Mode &&
			owned.ActiveID == current.Plan.ActiveID && owned.DraftID == current.Plan.DraftID {
			p.Plan = nil
		} else {
			p.Plan = &owned
		}
	}
	if p.Title == nil && p.LastPrompt == nil && p.Mode == nil && p.AutoPilot == nil && p.Group == nil && p.Tasks == nil && p.Plan == nil &&
		p.AdvisorSettings == nil {
		return nil, nil
	}
	return &p, nil
}
