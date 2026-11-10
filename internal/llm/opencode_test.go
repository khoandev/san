package llm

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"
	utls "github.com/refraction-networking/utls"

	"github.com/genai-io/san/internal/core"
)

// The gateway's free tier is gated on wire shape rather than on a credential,
// so these are the checks that decide whether a request is answered at all.

func TestOpenCodeSessionTokenHasTheGateShapeAndIsStable(t *testing.T) {
	pattern := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

	first := opencodeSessionToken("conv-1")
	if !pattern.MatchString(first) {
		t.Fatalf("token %q does not match the gate's shape", first)
	}
	if second := opencodeSessionToken("conv-1"); second != first {
		t.Errorf("the same conversation minted two tokens: %q then %q — every turn would miss the cache", first, second)
	}
	if other := opencodeSessionToken("conv-2"); other == first {
		t.Errorf("two conversations share one token: %q", other)
	}

	canonical := "ses_0123456789abABCDEFGHIJKLMN"
	if got := opencodeSessionToken(canonical); got != canonical {
		t.Errorf("a canonical token was rewritten: %q, want %q", got, canonical)
	}
}

// The head is clocked, not digest-derived: Zen decodes it as a client-side
// timestamp and refuses a value that reads as a foreign client.
func TestOpenCodeSessionHeadsAreClocked(t *testing.T) {
	heads := make(map[string]bool)
	for _, id := range []string{"a", "b", "c", "d"} {
		heads[opencodeSessionToken(id)[4:16]] = true
	}
	if len(heads) != 4 {
		t.Fatalf("four sessions produced %d distinct heads: %v", len(heads), heads)
	}
}

func TestOpenCodeFreeModelMatchesTheLineup(t *testing.T) {
	for _, id := range []string{"big-pickle", "beam-free", "deepseek-v4.1-flash-free"} {
		if !opencodeFreeModel(id) {
			t.Errorf("%q was not recognised as free", id)
		}
	}
	for _, id := range []string{"claude-opus-5", "deepseek-v4.1-flash", "free-tier", ""} {
		if opencodeFreeModel(id) {
			t.Errorf("%q was taken for a free model", id)
		}
	}
}

// Zen rotates its free lineup and keeps the models it retired from it, so the
// data still carries them. None of them is a choice San offers: a billed model
// is unreachable by policy, and a deprecated free one is a dead end.
func TestOpenCodeExposureKeepsCurrentFreeModelsOnly(t *testing.T) {
	models := []ai.Model{
		{ID: "big-pickle"},
		{ID: "glm-5.3-flash-free"},
		{ID: "glm-5-free", Stage: ai.StageDeprecated},
		{ID: "nemotron-3-super-free", Stage: ai.StageDeprecated},
		{ID: "claude-opus-5"},
		{ID: "gemini-3-pro", Stage: ai.StageDeprecated},
	}

	var kept []string
	for _, m := range openCodeOnly(opencodeVendorID, models) {
		kept = append(kept, m.ID)
	}
	want := []string{"big-pickle", "glm-5.3-flash-free"}
	if strings.Join(kept, ",") != strings.Join(want, ",") {
		t.Errorf("exposed %v, want %v", kept, want)
	}

	// Every other vendor keeps its deprecated models: there they are a warning
	// the picker states, not a dead end.
	if got := openCodeOnly("deepseek", models); len(got) != len(models) {
		t.Errorf("a non-Zen vendor lost models: %d of %d kept", len(got), len(models))
	}
}

func TestOpenCodeShapeRequestAddsTheEnvBlockAndGateToolsOnce(t *testing.T) {
	system := "base"
	var tools []core.ToolSchema
	opencodeShapeRequest("big-pickle", "conv-1", "/tmp/project", &system, &tools)

	want := "base\n<env>\n  Working directory: /tmp/project\n</env>"
	if system != want {
		t.Fatalf("system = %q, want %q", system, want)
	}
	for _, name := range opencodeGateTools {
		if count := countTool(tools, name); count != 1 {
			t.Errorf("tool %q appears %d times, want 1", name, count)
		}
	}

	// A second pass — a retried inference — must change neither.
	opencodeShapeRequest("big-pickle", "conv-1", "/tmp/elsewhere", &system, &tools)
	if system != want {
		t.Errorf("the env block moved on a second shape: %q", system)
	}
	for _, name := range opencodeGateTools {
		if count := countTool(tools, name); count != 1 {
			t.Errorf("tool %q appears %d times after a second shape, want 1", name, count)
		}
	}
}

func TestOpenCodeShapeRequestLeavesABilledModelAlone(t *testing.T) {
	system := "base"
	tools := []core.ToolSchema{{Name: "Read"}}
	opencodeShapeRequest("claude-opus-5", "conv-1", "/tmp/project", &system, &tools)

	if system != "base" {
		t.Errorf("a billed model's prompt was rewritten: %q", system)
	}
	if len(tools) != 1 {
		t.Errorf("a billed model's roster was padded: %+v", tools)
	}
}

func TestOpenCodeShapeRequestKeepsAnExistingRoster(t *testing.T) {
	system := "<env>\n  Working directory: /already\n</env>"
	tools := []core.ToolSchema{
		{Name: "bash"}, {Name: "edit"}, {Name: "glob"}, {Name: "grep"}, {Name: "read"},
	}
	opencodeShapeRequest("beam-free", "", "/tmp/ignored", &system, &tools)

	if system != "<env>\n  Working directory: /already\n</env>" {
		t.Errorf("the existing env block was replaced: %q", system)
	}
	if len(tools) != 5 {
		t.Errorf("a complete roster was padded to %d entries", len(tools))
	}
}

func TestOpenCodeSessionHeadersForAFreeModel(t *testing.T) {
	const cwd = "/tmp/project"
	headers := opencodeSessionHeaders("big-pickle", "conv-1", cwd)
	if got := headers["User-Agent"]; got != opencodeUserAgent {
		t.Errorf("User-Agent = %q, want %q", got, opencodeUserAgent)
	}
	if got := headers["x-opencode-client"]; got != opencodeClientName {
		t.Errorf("x-opencode-client = %q, want %q", got, opencodeClientName)
	}
	session := headers["x-opencode-session"]
	if !opencodeSessionPattern.MatchString(session) {
		t.Errorf("x-opencode-session = %q, want the canonical shape", session)
	}
	// The 2.2.6 CLI answers to four spellings of the affinity token, and the
	// gate reads the same token from each.
	for _, name := range []string{"x-opencode-session-id", "x-session-affinity", "X-Session-Id"} {
		if got := headers[name]; got != session {
			t.Errorf("%s = %q, want the session token %q", name, got, session)
		}
	}
	if got := headers["x-opencode-project"]; got != opencodeProjectID(cwd) {
		t.Errorf("x-opencode-project = %q, want the project's SHA-1", got)
	}

	// Traffic outside any conversation still carries the header, on this
	// installation's identity.
	if got := opencodeSessionHeaders("beam-free", "", cwd)["x-opencode-session"]; !opencodeSessionPattern.MatchString(got) {
		t.Errorf("a conversation-less session header = %q, want the canonical shape", got)
	}
}

// The project identity has to match what the CLI sends: SHA-1 of the canonical
// worktree root, not of the directory a turn happens to start in.
func TestOpenCodeProjectIDIsTheCanonicalRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "llm")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum([]byte(realRoot))
	want := hex.EncodeToString(sum[:])
	if got := opencodeProjectID(sub); got != want {
		t.Errorf("project ID from a subdirectory = %q, want %q", got, want)
	}
	if got := opencodeProjectID(root); got != want {
		t.Errorf("project ID from the root = %q, want %q", got, want)
	}
}

func countTool(tools []core.ToolSchema, name string) int {
	n := 0
	for _, schema := range tools {
		if schema.Name == name {
			n++
		}
	}
	return n
}

// TestOpenCodeHelloOffersOnlyHTTP1 guards the one property the gate is known
// to read besides the extension order: a hello that offers h2 puts the request
// on Go's HTTP/2 stack, which the free tier refuses.
func TestOpenCodeHelloOffersOnlyHTTP1(t *testing.T) {
	spec := opencodeClientHello()
	for _, extension := range spec.Extensions {
		if _, ok := extension.(*utls.GREASEEncryptedClientHelloExtension); ok {
			t.Fatalf("Bun sends no GREASE, got %T", extension)
		}
		alpn, ok := extension.(*utls.ALPNExtension)
		if !ok {
			continue
		}
		if len(alpn.AlpnProtocols) != 1 || alpn.AlpnProtocols[0] != "http/1.1" {
			t.Fatalf("ALPN = %v, want only http/1.1", alpn.AlpnProtocols)
		}
		return
	}
	t.Fatal("hello carries no ALPN extension")
}

// TestOpenCodeRoundTripperDropsSDKHeaders keeps the request from announcing a
// Go SDK, and keeps the caller's request usable after it is stripped.
func TestOpenCodeRoundTripperDropsSDKHeaders(t *testing.T) {
	var seen http.Header
	rt := opencodeRoundTripper{next: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Stainless-Lang", "go")
	req.Header.Set("X-Stainless-Package-Version", "3.88.0")
	req.Header.Set("X-Opencode-Client", "cli")
	if _, err := rt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["X-Stainless-Lang"]; ok {
		t.Fatalf("X-Stainless-Lang reached the wire: %v", seen)
	}
	if seen.Get("X-Opencode-Client") != "cli" {
		t.Fatalf("identity header lost: %v", seen)
	}
	if req.Header.Get("X-Stainless-Lang") != "go" {
		t.Fatal("the caller's request was mutated")
	}
}

// TestOpenCodeZenVendorBringsItsOwnClient pins the wiring: the credential and
// the transport are both the vendor's, not the caller's.
func TestOpenCodeZenVendorBringsItsOwnClient(t *testing.T) {
	var cfg endpoint.Config
	if err := configureOpenCodeZen(catalog.Vendor{}, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != opencodePublicKey {
		t.Fatalf("API key = %q, want %q", cfg.APIKey, opencodePublicKey)
	}
	transport, ok := cfg.HTTPClient.Transport.(opencodeRoundTripper)
	if !ok {
		t.Fatalf("transport = %T, want opencodeRoundTripper", cfg.HTTPClient.Transport)
	}
	if _, ok := transport.next.(*http.Transport); !ok {
		t.Fatalf("next = %T, want *http.Transport", transport.next)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
