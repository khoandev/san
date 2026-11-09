package subagent

import (
	"context"
	"strings"

	"github.com/genai-io/san/internal/tool"
)

// RouteRequest is what a Router sees of a delegation: the brief alone. Routing
// runs only for a call that named no definition and asked for no depth, so the
// brief is the only signal there is.
type RouteRequest struct {
	Prompt string
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
// Routing narrows, never widens. An unnamed call in a read-only parent keeps
// the posture default — the bundled explore definition — because substituting
// an edit charter would turn an investigation the user approved into a worker
// that writes. A named definition is never replaced, and a continuation keeps
// the policy it was approved under instead of being classified again.
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

	callerNamed := strings.TrimSpace(req.Name) != ""
	chosen := strings.TrimSpace(req.Name)
	if !callerNamed && tier == "" {
		verdict, ok := a.Executor.classify(ctx, req.Prompt)
		if !ok {
			return tool.AgentRoute{}, false
		}
		tier, chosen = verdict.tier, verdict.agent
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
		// The call is still unnamed: it keeps the posture it would have
		// inherited, and only its charter and rung change. A named call keeps
		// the name it wrote, so nothing is substituted for it.
		routed = chosen
		if chosen != "" {
			if posture, _, ok := a.ResolveAgentSelection(""); ok {
				info.PermissionMode = posture.PermissionMode
			}
		}
	}
	return tool.AgentRoute{Tier: tier, Agent: routed, Info: info, Raw: raw}, true
}

// classify asks the installed router for a verdict. It refuses to run without a
// router, and refuses in a read-only parent: the only edit definition a route
// could name would be refused by that posture anyway, so classification would
// spend a model call to learn nothing.
func (e *Executor) classify(ctx context.Context, prompt string) (routeVerdict, bool) {
	if e.router == nil || e.currentParentPermissionMode() == PermissionExplore {
		return routeVerdict{}, false
	}
	decision, ok := e.router.Route(ctx, RouteRequest{Prompt: prompt})
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
