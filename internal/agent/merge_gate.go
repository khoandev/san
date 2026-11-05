package agent

// This file places the advisor's pre-merge review in the permission path.
//
// The gate is resolved per call rather than captured when the agent is built:
// the runtime is rebuilt before the app re-wires its advisor, so a gate captured
// at build time would belong to the previous session — or to none at all.
//
// It sits in front of the whole permission gate, hooks included. A PreToolUse
// hook's "allow" waives the routine prompt and nothing more; the review a merge
// is required to pass is not a prompt, so nothing but the gate itself can waive
// it.

import (
	"context"
	"strings"

	"github.com/genai-io/san/internal/advisor"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool/perm"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
)

// MergeGateFunc resolves the advisor's merge gate for one tool call. A nil
// resolver, or one returning nil, leaves the permission path exactly as it was;
// the app returns nil while the advisor is off, torn down, or between sessions.
type MergeGateFunc func() *advisor.MergeGate

// withMergeGate wraps the agent's permission gate with the pre-merge review. A
// call the gate refuses never reaches the gate below it; anything else — a
// permit, a confirmed risk, a command that integrates nothing — continues
// through the ordinary path, so deny rules still apply to a reviewed merge.
func withMergeGate(resolve MergeGateFunc, pg *PermissionGate, next core.Gate) core.Gate {
	if resolve == nil {
		return next
	}
	return func(ctx context.Context, call sdkagent.PreToolContext) (sdkagent.Decision, error) {
		input, _ := core.ParseToolInput(call.Call.Input)
		if allow, reason, gated := mergeGateAnswer(ctx, resolve, pg, call.Call.Name, input); gated && !allow {
			return sdkagent.Decision{Block: true, Reason: reason}, nil
		}
		return next(ctx, call)
	}
}

// mergeGatedCheck wraps the permission check the tools carry in their context.
// A nested agent — a subagent, a fork — consults that checker instead of the
// agent gate, so a merge it runs has to pass the same review.
func mergeGatedCheck(resolve MergeGateFunc, pg *PermissionGate, base perm.PermissionFunc) perm.PermissionFunc {
	if resolve == nil {
		return base
	}
	return func(ctx context.Context, name string, input map[string]any) (bool, string) {
		if allow, reason, gated := mergeGateAnswer(ctx, resolve, pg, name, input); gated && !allow {
			return false, reason
		}
		return base(ctx, name, input)
	}
}

// mergeGateAnswer asks the advisor about one call and turns a "confirm" into the
// human prompt the TUI already owns: the review never prompts from its own
// goroutine, and the permission path is the one place that may block on the
// user. gated reports whether the gate judged the call at all.
func mergeGateAnswer(ctx context.Context, resolve MergeGateFunc, pg *PermissionGate, name string, input map[string]any) (allow bool, reason string, gated bool) {
	gate := resolve()
	if gate == nil {
		return false, "", false
	}
	out := gate.Check(ctx, name, input)
	switch out.Verdict {
	case advisor.MergeReject:
		return false, out.Reason, true
	case advisor.MergeConfirm:
		if pg == nil {
			// Nothing can ask the human, so a confirmation is not available.
			return false, out.Reason, true
		}
		allow, reason := pg.prompt(ctx, &PermGateRequest{ToolName: name, Description: out.Reason, Input: input})
		if !allow && strings.TrimSpace(reason) == "" {
			reason = out.Reason
		}
		return allow, reason, true
	case advisor.MergePermit:
		return true, out.Reason, true
	}
	return false, "", false
}
