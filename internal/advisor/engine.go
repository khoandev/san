// Package advisor owns San's independent reviewer: a read-only fork with its
// own model and conversation that reads what the main agent just did and, when
// it has something worth saying, returns one short note for the main
// conversation.
//
// The package is domain-only: it runs one inference with read tools and returns
// text. The app owns when to run it, how the transcript delta is rendered, and
// how the note is delivered back. A review never writes code, never prompts the
// TUI and never shares the main agent's conversation.
package advisor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/tool"
	"github.com/genai-io/san/internal/tool/perm"
)

const (
	// DefaultTimeout bounds one review end to end. A review is background work:
	// a confused fork must not loop and a hung provider must not pin the
	// goroutine forever.
	DefaultTimeout = 3 * time.Minute
	// defaultMaxSteps caps the fork's inference steps: enough to read a few
	// files the transcript points at, not enough to explore a codebase.
	defaultMaxSteps = 12
	// maxNoteRunes caps the note injected back into the main conversation, so a
	// runaway reviewer cannot flood the context window.
	maxNoteRunes = 4000
)

// ReadOnlyToolNames are the only tools a review may use. It is a superset of
// what this tree registers — Grep and Glob are listed for the platforms that
// have them — and ReadOnlyTools filters it against the registry, so a missing
// tool simply does not appear rather than failing the review.
var ReadOnlyToolNames = []string{"Read", "Grep", "Glob"}

// EngineConfig carries everything one engine needs to run a review.
type EngineConfig struct {
	// Client resolves the model for one inference. Required.
	Client func(msgs []core.Message) (*ai.Client, error)
	// CallOptions are per-call settings asked for fresh each inference.
	CallOptions func() []ai.Option
	// ShapeInference settles the wire shape of one inference for its vendor.
	ShapeInference func(system string, tools []core.ToolSchema) (string, []core.ToolSchema)
	// Tools is the read-only tool set the fork may use. Nil builds the default
	// read-only set rooted at CWD.
	Tools *core.Tools
	// Model names the review model; recorded in diagnostics, never guessed.
	Model string
	// Timeout bounds one review end to end. Zero uses DefaultTimeout.
	Timeout time.Duration
	// CWD roots the read-only tools.
	CWD string
	// OnEvent observes the fork's lifecycle events, for a sidechain writer.
	OnEvent func(core.Event)
}

// Engine runs one review. It holds no state: every run is a fresh fork with its
// own system prompt, tool set and deadline, so two reviews never share a
// conversation.
type Engine struct {
	cfg EngineConfig
}

// NewEngine returns an engine over cfg. A config without a client fails at
// Advise rather than here, so construction cannot panic a session that never
// reviews.
func NewEngine(cfg EngineConfig) *Engine { return &Engine{cfg: cfg} }

// Advise reviews one transcript delta and returns the note worth injecting into
// the main conversation, or "" when there is nothing to say.
//
// An error means the review did not run to an answer — a provider failure, a
// timeout. Callers log it; a review that could not run says nothing about the
// work, which is why it is never treated as approval.
func (e *Engine) Advise(ctx context.Context, transcript string) (string, error) {
	if e == nil {
		return "", fmt.Errorf("advisor engine is nil")
	}
	if e.cfg.Client == nil {
		return "", fmt.Errorf("advisor engine has no client")
	}

	timeout := e.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

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
		Render: func() string { return systemPrompt },
	}, "advisor")

	ag := core.NewAgent(core.Config{
		Client:         e.cfg.Client,
		CallOptions:    e.cfg.CallOptions,
		ShapeInference: e.cfg.ShapeInference,
		System:         sys,
		Tools:          tools,
		// Static allowlist, no interactive resolver: a review must never prompt
		// the TUI from its background goroutine.
		Gate:      tool.Permission(readOnlyGate(tools)),
		MaxSteps:  defaultMaxSteps,
		OutboxBuf: -1, // headless fork, driven through ThinkAct
		OnEvent:   e.cfg.OnEvent,
	})
	ag.Append(core.UserMessage(reviewPrompt(transcript), nil))

	res, err := ag.ThinkAct(ctx)
	if err != nil {
		return "", fmt.Errorf("advisor review: %w", err)
	}
	if res == nil {
		return "", nil
	}
	return parseNote(res.Content), nil
}

// parseNote turns the model's answer into the note to inject. NO_ADVICE (with
// any surrounding punctuation or casing) means silence, and a run of markdown
// code fences around the note is stripped so the conversation reads cleanly.
func parseNote(content string) string {
	s := strings.TrimSpace(content)
	if s == "" {
		return ""
	}
	s = stripFence(s)
	if s == "" {
		return ""
	}
	firstLine, _, _ := strings.Cut(s, "\n")
	if strings.EqualFold(strings.Trim(firstLine, " \t.!,;:"), "NO_ADVICE") {
		return ""
	}
	if runes := []rune(s); len(runes) > maxNoteRunes {
		s = string(runes[:maxNoteRunes]) + "…"
	}
	return s
}

// stripFence removes one enclosing markdown code fence, if the model added one
// around a plain-text note.
func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	body := strings.TrimPrefix(s, "```")
	if newline := strings.IndexByte(body, '\n'); newline >= 0 {
		body = body[newline+1:]
	}
	body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	return strings.TrimSpace(body)
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
