package advisor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/reviewer"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

// Review depths, mirroring the settings values ("fast", "standard", "deep").
// They are repeated here rather than imported so the domain package stays free
// of the settings layer; the wiring maps one to the other.
const (
	ModeFast     = "fast"
	ModeStandard = "standard"
	ModeDeep     = "deep"
)

// Bounds every review carries regardless of configuration: a review is
// background work, so a confused fork must not loop, and a hung provider must
// not pin the goroutine forever.
const (
	// defaultReviewTimeout bounds one review end to end when the engine config
	// sets no timeout.
	defaultReviewTimeout = 3 * time.Minute
	// defaultMaxFindings caps one review's list when nothing else does, so a
	// runaway reviewer cannot flood the panel.
	defaultMaxFindings = 20
	// maxReviewSteps caps the fork's inference steps: enough to read the diff
	// and the few files it points at, not enough to explore a codebase.
	maxReviewSteps = 12
)

// ReadOnlyToolNames are the only tools a review may use. It is a superset of
// what this tree registers — Grep and Glob are listed for the platforms that
// have them — and ReadOnlyTools filters it against the registry, so a missing
// tool simply does not appear rather than failing the review.
var ReadOnlyToolNames = []string{"Read", "Grep", "Glob"}

// EngineConfig carries everything one engine needs to run reviews. The client,
// call options and shape inference come from the app's provider wiring; the
// policy is the block policy resolved from settings.
type EngineConfig struct {
	// Client resolves the model for one inference. Required.
	Client func(msgs []core.Message) (*ai.Client, error)
	// CallOptions are per-call settings asked for fresh each inference.
	CallOptions func() []ai.Option
	// ShapeInference settles the wire shape of one inference for its vendor.
	ShapeInference func(system string, tools []core.ToolSchema) (string, []core.ToolSchema)
	// Tools is the read-only tool set the fork may use. Nil builds the default
	// read-only set (see ReadOnlyTools) rooted at CWD.
	Tools *core.Tools
	// Policy resolves the review's status from its findings. The zero value is
	// advisory only: nothing blocks.
	Policy Policy
	// Model names the review model; recorded on the review, never guessed.
	Model string
	// Mode is ModeFast, ModeStandard or ModeDeep. Empty means ModeStandard.
	Mode string
	// MaxTokens caps one review's output. Zero leaves the cap to CallOptions.
	MaxTokens int
	// Timeout bounds one review end to end. Zero uses defaultReviewTimeout.
	Timeout time.Duration
	// MaxFindings caps one review's finding list. Zero uses
	// defaultMaxFindings; findings beyond it are dropped from the least severe
	// end.
	MaxFindings int
	// CWD roots the read-only tools.
	CWD string
	// OnEvent observes the fork's lifecycle events, for a sidechain writer.
	OnEvent func(core.Event)
}

// Engine runs one advisor review at a time. It holds no state: every run is a
// fresh fork with its own system prompt, tool set and deadline, so two reviews
// never share a conversation.
type Engine struct {
	cfg EngineConfig
}

// NewEngine returns an engine over cfg. A config without a client fails at Run
// rather than here, so construction cannot panic a session that never reviews.
func NewEngine(cfg EngineConfig) *Engine { return &Engine{cfg: cfg} }

// Request is one review's subject and context: what to look at, what it was
// supposed to accomplish, and what was already decided.
type Request struct {
	// Trigger names why the review is running; it also selects which action
	// the policy gates.
	Trigger Trigger
	// Target identifies the reviewed object (task, step, commit, diff range).
	Target Target
	// Diff is the unified diff text under review. Empty for a review that is
	// only about a result or a set of checks.
	Diff string
	// DiffHash identifies Diff, so a re-run of the same change is detectable.
	DiffHash string
	// Files lists the changed paths, when the diff read knows them.
	Files []string
	// DiffTruncated reports that Diff was cut for size; the reviewer is told so
	// it does not treat a missing hunk as missing work.
	DiffTruncated bool
	// Goal is what the work was supposed to accomplish.
	Goal string
	// Acceptance lists the criteria the work is judged against.
	Acceptance []string
	// Checks records test, lint, typecheck and build results.
	Checks []string
	// Decisions records choices already settled in the session; a change that
	// contradicts one is a finding.
	Decisions []string
	// Constraints records known limits and risks.
	Constraints []string
	// CustomPrompt appends the user's own review criteria.
	CustomPrompt string
	// Mode overrides the engine's review depth for this run.
	Mode string
	// MaxFindings overrides the engine's cap for this run.
	MaxFindings int
}

// Run forks a read-only reviewer, runs one review, and returns the review with
// its status resolved by the policy. The model's own claimed status is parsed
// and discarded: a review that says "passed" while listing a P0 must not pass.
//
// An error means the review did not produce a verdict — a provider failure, a
// timeout, or an answer that is not the expected JSON. Callers record that as a
// StatusError review rather than treating it as a pass.
func (e *Engine) Run(ctx context.Context, req Request) (Review, error) {
	if e == nil {
		return Review{}, fmt.Errorf("advisor engine is nil")
	}
	if e.cfg.Client == nil {
		return Review{}, fmt.Errorf("advisor engine has no client")
	}

	timeout := e.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultReviewTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mode := normalizeMode(firstNonEmpty(req.Mode, e.cfg.Mode))
	maxFindings := req.MaxFindings
	if maxFindings <= 0 {
		maxFindings = e.cfg.MaxFindings
	}
	if maxFindings <= 0 {
		maxFindings = defaultMaxFindings
	}

	tools := e.cfg.Tools
	if tools == nil {
		tools = ReadOnlyTools(e.cfg.CWD)
	}

	// The advisor owns its system prompt: it must not inherit the main agent's,
	// which describes a different job (writing code) and carries instructions
	// the reviewer must not follow.
	sys := core.NewSystem()
	sys.Use(core.Section{
		Slot:   core.SlotIdentity,
		Name:   "advisor-identity",
		Source: core.Predefined,
		Render: func() string { return systemPrompt(mode) },
	}, "advisor")

	review := Review{
		ID:        NewID("review"),
		Trigger:   req.Trigger,
		Target:    req.Target.Clone(),
		DiffHash:  req.DiffHash,
		Model:     e.cfg.Model,
		StartedAt: time.Now(),
	}

	ag := core.NewAgent(core.Config{
		Client:         e.cfg.Client,
		CallOptions:    e.callOptions(),
		ShapeInference: e.cfg.ShapeInference,
		System:         sys,
		Tools:          tools,
		// Static allowlist, no interactive resolver: a review must never prompt
		// the TUI from its background goroutine.
		Gate:      tool.Permission(readOnlyGate(tools)),
		MaxSteps:  maxReviewSteps,
		OutboxBuf: -1, // headless fork, driven through ThinkAct
		OnEvent:   e.cfg.OnEvent,
	})
	ag.Append(core.UserMessage(reviewPrompt(req, mode), nil))

	res, err := ag.ThinkAct(ctx)
	if err != nil {
		return Review{}, fmt.Errorf("advisor review: %w", err)
	}
	content := ""
	if res != nil {
		content = res.Content
	}

	findings, summary, err := parseReview(content)
	if err != nil {
		return Review{}, err
	}

	review.Summary = summary
	review.Findings = findings
	normalizeReview(&review)
	if len(review.Findings) > maxFindings {
		review.Findings = review.Findings[:maxFindings]
	}
	review.FinishedAt = time.Now()
	review.ApplyPolicy(e.cfg.Policy)
	if err := review.Validate(); err != nil {
		return Review{}, fmt.Errorf("advisor review is incoherent: %w", err)
	}
	return review, nil
}

// callOptions returns the per-call options, adding the output cap when the
// config sets one.
func (e *Engine) callOptions() func() []ai.Option {
	if e.cfg.MaxTokens <= 0 {
		return e.cfg.CallOptions
	}
	return func() []ai.Option {
		var opts []ai.Option
		if e.cfg.CallOptions != nil {
			opts = append(opts, e.cfg.CallOptions()...)
		}
		return append(opts, ai.WithMaxTokens(e.cfg.MaxTokens))
	}
}

// ReadOnlyTools builds the advisor's tool set: the allowlisted read tools that
// this binary actually registers, rooted at cwd. Nothing that writes, runs a
// command, spawns an agent or asks the user is ever included.
func ReadOnlyTools(cwd string) *core.Tools {
	allowed := make(map[string]bool, len(ReadOnlyToolNames))
	for _, name := range ReadOnlyToolNames {
		allowed[name] = true
	}
	var schemas []core.ToolSchema
	for _, schema := range tool.GetToolSchemas() {
		if allowed[schema.Name] {
			schemas = append(schemas, schema)
		}
	}
	return tool.AdaptToolRegistry(schemas, func() string { return cwd })
}

// readOnlyGate builds a static permission policy over the tool set: a tool in
// the set passes, everything else rejects with a reason the model can read.
// There is deliberately no interactive resolver.
func readOnlyGate(tools *core.Tools) perm.PermissionFunc {
	names := make(map[string]bool)
	for _, t := range tools.All() {
		names[t.Schema().Name] = true
	}
	return func(_ context.Context, name string, _ map[string]any) (bool, string) {
		if names[name] {
			return true, ""
		}
		return false, "tool not permitted for the advisor reviewer"
	}
}

// modelAnswer is the JSON shape a review is asked for. Status is accepted and
// ignored: the verdict is computed from the findings, never taken from the
// model, so a claim and its own findings cannot contradict each other.
type modelAnswer struct {
	Status   string         `json:"status"`
	Summary  string         `json:"summary"`
	Findings []modelFinding `json:"findings"`
}

type modelFinding struct {
	Severity   string `json:"severity"`
	Title      string `json:"title"`
	Detail     string `json:"detail"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Evidence   string `json:"evidence"`
	Impact     string `json:"impact"`
	Suggestion string `json:"suggestion"`
}

// parseReview extracts the answer object from the model's text. Anything that
// is not a JSON object with recognizable severities is an error: a garbled
// answer is not a pass, and guessing what it meant would put an unverified
// verdict in the transcript.
func parseReview(content string) ([]Finding, string, error) {
	raw := reviewer.ExtractJSONObject(content)
	if raw == "" {
		return nil, "", fmt.Errorf("advisor answer has no JSON object: %q", clip(content, 200))
	}
	var answer modelAnswer
	if err := json.Unmarshal([]byte(raw), &answer); err != nil {
		return nil, "", fmt.Errorf("parse advisor answer: %w", err)
	}
	findings := make([]Finding, 0, len(answer.Findings))
	for i, mf := range answer.Findings {
		severity, ok := ParseSeverity(mf.Severity)
		if !ok {
			return nil, "", fmt.Errorf("advisor finding %d has unknown severity %q", i+1, mf.Severity)
		}
		title := strings.TrimSpace(mf.Title)
		detail := strings.TrimSpace(mf.Detail)
		if title == "" {
			title = firstLine(detail)
		}
		if title == "" {
			title = "Untitled finding"
		}
		findings = append(findings, Finding{
			Severity:   severity,
			Title:      title,
			Detail:     detail,
			File:       strings.TrimSpace(mf.File),
			Line:       max(mf.Line, 0),
			Evidence:   strings.TrimSpace(mf.Evidence),
			Impact:     strings.TrimSpace(mf.Impact),
			Suggestion: strings.TrimSpace(mf.Suggestion),
			Status:     FindingOpen,
		})
	}
	return findings, strings.TrimSpace(answer.Summary), nil
}

// normalizeMode maps an unset or unrecognized depth to the standard one.
func normalizeMode(mode string) string {
	switch mode {
	case ModeFast, ModeDeep:
		return mode
	default:
		return ModeStandard
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
