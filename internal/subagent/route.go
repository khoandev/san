package subagent

import (
	"context"
	"strings"

	"github.com/genai-io/san/internal/tool"
)

// RouteRequest is what a Router sees of a delegation: the brief, and the
// posture the call would inherit. It carries ReadOnly so a classifier can pick
// from the definitions that posture can actually run — a read-only session may
// still route to explore or reviewer, and must never be offered an edit
// charter. Routing runs only for a call that named no definition and asked for
// no depth, so the brief is the only description of the work there is.
type RouteRequest struct {
	Prompt   string
	ReadOnly bool
}

// RouteDecision is a Router's answer: the rung the brief is for, and optionally
// the definition that suits it. An empty Tier means "no opinion", which routes
// nothing; an empty Agent keeps the definition the call would have run anyway.
type RouteDecision struct {
	Tier  string
	Agent string
}

// Router classifies a delegation brief. It is optional: without one, calls
// route only through a name or a tier the caller supplied.
//
// A router must answer, not fail. Classification is an optimization, so a
// timeout, a provider error or an unusable verdict belongs in the decision —
// standard tier — rather than in an error the Agent call would carry. ok=false
// means "do not route this call" and honest hosts return it only when they
// have nothing to say.
type Router interface {
	Route(ctx context.Context, req RouteRequest) (RouteDecision, bool)
}

// routeVerdict is a router answer this package can act on: a valid rung, and a
// definition name the router suggested.
type routeVerdict struct {
	tier  string
	agent string
}

// SetRouter installs the optional brief classifier. It is set on the adapter so
// a caller that holds only the adapter can wire one, and it lands on the
// executor so approval and execution consult the same router.
func (a *ExecutorAdapter) SetRouter(r Router) {
	a.Executor.router = r
}

// RouteAgent is the routing contract the Agent tool pins at approval time. It
// answers what the call will run as: the definition, the rung, the model shown
// to the user, and the exact configuration execution must use.
//
// A name this host recognizes is a caller decision; any other name only labels
// the task, so the call is classified like an unnamed one and the label travels
// with the route. Routing narrows, never widens: a read-only parent is still
// classified — a review belongs to reviewer even in plan mode — but only the
// read-only definitions are on offer, so an unnamed call there keeps the
// posture default (the bundled explore definition) rather than turning an
// investigation the user approved into a worker that writes. A named definition
// is never replaced, and a continuation keeps the policy it was approved under
// instead of being classified again.
func (a *ExecutorAdapter) RouteAgent(ctx context.Context, req tool.AgentRouteRequest) (tool.AgentRoute, bool) {
	if req.Resume {
		return tool.AgentRoute{}, false
	}
	parent := a.Executor.currentParentPermissionMode()
	tier := string(NormalizeAgentTier(req.Tier))
	if strings.TrimSpace(req.Tier) != "" && tier == "" {
		// The tool rejects an unknown tier before routing; a direct caller gets
		// no guess rather than a silent fallback.
		return tool.AgentRoute{}, false
	}

	name := strings.TrimSpace(req.Name)
	callerNamed := a.Executor.knownDefinition(name)
	chosen, label := "", ""
	if callerNamed {
		chosen = name
	} else {
		label = name
	}
	if !callerNamed && tier == "" {
		verdict, ok := a.Executor.classify(ctx, RouteRequest{Prompt: req.Prompt}, parent)
		if !ok {
			return tool.AgentRoute{}, false
		}
		tier, chosen = verdict.tier, verdict.agent
	}
	if !callerNamed && chosen != "" {
		// A verdict this host cannot vouch for is dropped for the rung's own
		// definition instead of being run: a classifier may not hand a session
		// a charter routing itself would have refused.
		chosen = a.Executor.routableDefinition(chosen, parent)
	}
	if !callerNamed && chosen == "" {
		chosen = a.Executor.tierDefinition(tier, parent)
	}

	info, raw, ok := a.ResolveAgentSelection(chosen)
	if !ok {
		return tool.AgentRoute{}, false
	}
	if tier != "" {
		// Approval shows the model the rung sends this work to: the same
		// reference execution will ask for, so the two cannot disagree.
		if config, ok := raw.(*AgentConfig); ok {
			info.Model = a.Executor.effectiveModelNameAtTier(config, chosen, tier)
		}
	}
	routed := ""
	if !callerNamed {
		// The call named no definition: it keeps the posture it would have
		// inherited, and only its charter and rung change. A named call keeps
		// the name it wrote, so nothing is substituted for it.
		routed = chosen
		if chosen != "" {
			if posture, _, ok := a.ResolveAgentSelection(""); ok {
				info.PermissionMode = posture.PermissionMode
			}
			// A read-only definition is not a posture the call could have
			// inherited: approval must show the ceiling execution will apply.
			if config, ok := raw.(*AgentConfig); ok && NormalizePermissionMode(string(config.PermissionMode)) == PermissionExplore {
				info.PermissionMode = string(PermissionExplore)
			}
		}
	}
	return tool.AgentRoute{Tier: tier, Agent: routed, Label: label, Info: info, Raw: raw}, true
}

// knownDefinition reports whether name names a definition this host would run.
// An unrecognized name is a task label: the model wanted to name the work, not
// to pick a charter, so the call is still classified and the label survives as
// display identity. A disabled definition is not known either — the call must
// not be routed to what the user turned off, and only a name that resolves is
// allowed to decide for itself.
func (e *Executor) knownDefinition(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || e.registry == nil {
		return false
	}
	_, exists, enabled := e.registry.LookupAgent(name)
	return exists && enabled
}

// classify asks the installed router for a verdict. A read-only parent is
// classified like any other: investigation is most of what such a session does,
// and the router is what tells an investigation from a review. ReadOnly travels
// with the request so the router offers only definitions that posture can run;
// it is derived from the parent posture RouteAgent already read, so one launch
// reads the session mode once and the two cannot disagree.
//
// The verdict's definition is not trusted here; RouteAgent runs it through
// routableDefinition before it can substitute anything.
func (e *Executor) classify(ctx context.Context, req RouteRequest, parent PermissionMode) (routeVerdict, bool) {
	if e.router == nil {
		return routeVerdict{}, false
	}
	req.ReadOnly = req.ReadOnly || parent == PermissionExplore
	decision, ok := e.router.Route(ctx, req)
	if !ok {
		return routeVerdict{}, false
	}
	tier := string(NormalizeAgentTier(decision.Tier))
	if tier == "" || !ValidAgentTier(AgentTier(tier)) {
		// A verdict nobody can act on still routes, at the safe rung: the work
		// must not fall back to whatever model the session happens to hold.
		tier = string(TierStandard)
	}
	return routeVerdict{tier: tier, agent: strings.TrimSpace(decision.Agent)}, true
}

// routableDefinition validates a definition a router suggested. It must exist
// and be enabled, and a read-only parent may only be given a read-only one, so
// a classifier cannot hand a writing worker to a session that cannot approve it.
func (e *Executor) routableDefinition(name string, parent PermissionMode) string {
	name = strings.TrimSpace(name)
	if name == "" || e.registry == nil {
		return ""
	}
	config, exists, enabled := e.registry.LookupAgent(name)
	if !exists || !enabled {
		return ""
	}
	if parent == PermissionExplore && NormalizePermissionMode(string(config.PermissionMode)) != PermissionExplore {
		return ""
	}
	return config.Name
}

// tierDefinition names the bundled definition that fits a rung for an unnamed
// call. Deep has no separate edit definition: the rung is carried by the model
// role, so it runs the standard charter at the deeper model. A read-only parent
// gets no substitution at all — its unnamed call already runs the read-only
// default, and routing must never turn an investigation into an edit.
func (e *Executor) tierDefinition(tier string, parent PermissionMode) string {
	if tier == "" || parent == PermissionExplore || e.registry == nil {
		return ""
	}
	var candidate string
	switch AgentTier(tier) {
	case TierFast:
		candidate = "quick"
	case TierStandard, TierDeep:
		candidate = "task"
	}
	return e.routableDefinition(candidate, parent)
}
