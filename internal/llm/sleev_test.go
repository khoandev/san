package llm

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/san/internal/secret"
)

// testSleevGate is one enabled gateway, as loadSleevGate would resolve it.
func testSleevGate() *sleevGate {
	return &sleevGate{baseURL: "http://127.0.0.1:17321", harness: "pi"}
}

func mustFindVendor(t *testing.T, id string) catalog.Vendor {
	t.Helper()
	vendor, ok := findVendor(id)
	if !ok {
		t.Fatalf("findVendor(%q) not found", id)
	}
	return vendor
}

// Routing moves the address and says the three things the gateway needs to
// know — while leaving the protocol and the credential alone. The gateway
// forwards the key it is handed, so the key has to stay the provider's own.
func TestSleevGateRoutesThroughTheGateway(t *testing.T) {
	vendor := mustFindVendor(t, "moonshot")
	cfg := endpoint.Config{APIKey: "sk-upstream", BaseURL: vendor.BaseURL, API: vendor.API}

	if err := testSleevGate().route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}

	if cfg.BaseURL != "http://127.0.0.1:17321" {
		t.Fatalf("BaseURL = %q, want the gateway address", cfg.BaseURL)
	}
	if cfg.APIKey != "sk-upstream" {
		t.Fatalf("APIKey = %q, want the provider's own credential", cfg.APIKey)
	}
	if cfg.API != vendor.API {
		t.Fatalf("API = %q, want the vendor's own protocol %q", cfg.API, vendor.API)
	}
	if got, ok := cfg.Headers["Authorization"]; ok {
		t.Fatalf("Authorization = %q, want none: routing adds no credential of its own", got)
	}
	// Moonshot is one of the upstreams the gateway meters by name.
	for name, want := range map[string]string{
		sleevProviderHeader: "moonshotai",
		sleevHarnessHeader:  "pi",
		sleevBaseURLHeader:  "https://api.moonshot.cn/v1",
	} {
		if got := cfg.Headers[name]; got != want {
			t.Fatalf("header %s = %q, want %q", name, got, want)
		}
	}
}

// An endpoint address with a trailing slash must not produce a doubled path
// once the gateway appends the request's own.
func TestSleevGateTrimsTheUpstreamSlash(t *testing.T) {
	vendor := mustFindVendor(t, "moonshot")
	cfg := endpoint.Config{BaseURL: vendor.BaseURL + "//"}

	if err := testSleevGate().route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}
	if got := cfg.Headers[sleevBaseURLHeader]; got != vendor.BaseURL {
		t.Fatalf("%s = %q, want %q", sleevBaseURLHeader, got, vendor.BaseURL)
	}
}

// The vendors that state no address — the drivers hold the default privately —
// still have to be routed, so the protocol's own host answers for them.
func TestSleevGateFallsBackToTheProtocolHost(t *testing.T) {
	tests := []struct {
		vendorID string
		want     string
		family   string
	}{
		{vendorID: "anthropic", want: "https://api.anthropic.com", family: "anthropic"},
		{vendorID: "openai", want: "https://api.openai.com/v1", family: "openai"},
		{vendorID: "google", want: "https://generativelanguage.googleapis.com", family: "google"},
	}
	for _, tt := range tests {
		t.Run(tt.vendorID, func(t *testing.T) {
			vendor := mustFindVendor(t, tt.vendorID)
			// The premise: the row states nothing, which is what a driver fills in.
			if vendor.BaseURL != "" {
				t.Fatalf("%s states base URL %q; the fallback is not what this test covers", tt.vendorID, vendor.BaseURL)
			}

			cfg := endpoint.Config{APIKey: "sk-upstream"}
			if err := testSleevGate().route(&cfg, vendor); err != nil {
				t.Fatalf("route() error = %v", err)
			}
			if got := cfg.Headers[sleevBaseURLHeader]; got != tt.want {
				t.Fatalf("%s = %q, want %q", sleevBaseURLHeader, got, tt.want)
			}
			if got := cfg.Headers[sleevProviderHeader]; got != tt.family {
				t.Fatalf("%s = %q, want %q", sleevProviderHeader, got, tt.family)
			}
			if cfg.BaseURL != "http://127.0.0.1:17321" {
				t.Fatalf("BaseURL = %q, want the gateway address", cfg.BaseURL)
			}
		})
	}
}

// The gateway names several upstreams it serves itself, and OpenCode Zen is
// one of them: routing it as "openai" would send Zen's traffic to OpenAI. A
// custom endpoint is the opposite case — the gateway has never heard of it, so
// it routes by family and reaches it through the base URL San states.
func TestSleevGateNamesTheVendorsTheGatewayKnows(t *testing.T) {
	zen := mustFindVendor(t, opencodeVendorID)
	tests := []struct {
		name         string
		vendor       catalog.Vendor
		cfg          endpoint.Config
		wantProvider string
		wantBaseURL  string
	}{
		{
			name:   "opencode zen is its own provider",
			vendor: zen,
			// The factory fills cfg.BaseURL from the row, as it does here.
			cfg:          endpoint.Config{APIKey: opencodePublicKey, BaseURL: zen.BaseURL},
			wantProvider: "opencode",
			wantBaseURL:  opencodeBaseURL,
		},
		{
			name: "a custom endpoint routes by family and its own base URL",
			vendor: catalog.Vendor{
				ID:      string(CustomProvider),
				API:     ai.APIOpenAIChat,
				BaseURL: "https://llm.example.test/v1",
			},
			cfg:          endpoint.Config{APIKey: "sk-custom", BaseURL: "https://llm.example.test/v1"},
			wantProvider: "openai",
			wantBaseURL:  "https://llm.example.test/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if err := testSleevGate().route(&cfg, tt.vendor); err != nil {
				t.Fatalf("route() error = %v", err)
			}
			if got := cfg.Headers[sleevProviderHeader]; got != tt.wantProvider {
				t.Fatalf("%s = %q, want %q", sleevProviderHeader, got, tt.wantProvider)
			}
			if got := cfg.Headers[sleevBaseURLHeader]; got != tt.wantBaseURL {
				t.Fatalf("%s = %q, want %q", sleevBaseURLHeader, got, tt.wantBaseURL)
			}
		})
	}
}

// Routing adds headers; it must not take away the ones a vendor states, which
// are the client identity the gateway forwards upstream.
func TestSleevGateKeepsTheVendorsOwnHeaders(t *testing.T) {
	vendor := mustFindVendor(t, opencodeVendorID)
	if len(vendor.Headers) == 0 {
		t.Fatal("opencode vendor states no headers; this test needs one that does")
	}
	cfg := endpoint.Config{BaseURL: vendor.BaseURL}

	if err := testSleevGate().route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}
	for name, want := range vendor.Headers {
		if got := cfg.Headers[name]; got != want {
			t.Fatalf("vendor header %s = %q, want %q", name, got, want)
		}
	}
	if got := cfg.Headers[sleevBaseURLHeader]; got != opencodeBaseURL {
		t.Fatalf("%s = %q, want %q", sleevBaseURLHeader, got, opencodeBaseURL)
	}
}

// A deployment whose address San cannot state must fail loudly: routing it to
// the gateway anyway would send the request somewhere the gateway cannot
// forward, and silently skipping the gateway would hide that sleev is on.
func TestSleevGateRefusesAnUnknownUpstream(t *testing.T) {
	tests := []struct {
		name   string
		vendor catalog.Vendor
		cfg    endpoint.Config
		want   string
	}{
		{
			name:   "vertex deployment",
			vendor: catalog.Vendor{ID: "anthropic-vertex", API: ai.APIAnthropicVertex},
			cfg:    endpoint.Config{Vertex: &ai.VertexConfig{Project: "a-project"}},
			want:   "anthropic-vertex",
		},
		{
			name:   "vertex protocol without a project",
			vendor: catalog.Vendor{ID: "google-vertex", API: ai.APIGoogleVertex},
			want:   "google-vertex",
		},
		{
			name:   "protocol the gateway does not speak",
			vendor: catalog.Vendor{ID: "nowhere", API: ai.API("exotic-messages")},
			want:   "nowhere",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.APIKey = "sk-upstream"
			err := testSleevGate().route(&cfg, tt.vendor)
			if err == nil {
				t.Fatal("route() error = nil, want a refusal")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("route() error = %q, want it to name %q", err, tt.want)
			}
			// Nothing is half-applied on the way to the refusal.
			if cfg.BaseURL != tt.cfg.BaseURL || cfg.APIKey != "sk-upstream" || cfg.Headers != nil {
				t.Fatalf("refused config was mutated: %#v", cfg)
			}
		})
	}
}

// The common case: no gateway, so the endpoint the factory built stands.
func TestNilSleevGateLeavesTheEndpointAlone(t *testing.T) {
	vendor := mustFindVendor(t, "moonshot")
	cfg := endpoint.Config{APIKey: "sk-upstream", BaseURL: vendor.BaseURL}

	var gate *sleevGate
	if err := gate.route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}
	if cfg.BaseURL != vendor.BaseURL || cfg.APIKey != "sk-upstream" || cfg.Headers != nil {
		t.Fatalf("config changed with no gateway enabled: %#v", cfg)
	}
}

// The gate is read from the store at the moment a provider is opened, which is
// what makes a settings change take effect without a restart.
func TestLoadSleevGateReadsTheStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)

	if gate := loadSleevGate(); gate != nil {
		t.Fatalf("loadSleevGate() = %#v, want nil before the user configures one", gate)
	}

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.SetSleev(SleevConfig{Enabled: true, BaseURL: "http://127.0.0.1:9999"}); err != nil {
		t.Fatalf("SetSleev() error = %v", err)
	}

	gate := loadSleevGate()
	if gate == nil {
		t.Fatal("loadSleevGate() = nil, want the enabled gateway")
	}
	if gate.baseURL != "http://127.0.0.1:9999" {
		t.Fatalf("baseURL = %q, want the stored address", gate.baseURL)
	}
	if gate.harness != DefaultSleevHarness {
		t.Fatalf("harness = %q, want %q", gate.harness, DefaultSleevHarness)
	}

	if err := store.SetSleev(SleevConfig{Enabled: false}); err != nil {
		t.Fatalf("SetSleev(disabled) error = %v", err)
	}
	if gate := loadSleevGate(); gate != nil {
		t.Fatalf("loadSleevGate() = %#v after disabling, want nil", gate)
	}
}

// Enabling the gateway adds no provider: it is a route every provider may take,
// not a vendor the user connects. So nothing resolves through it, and
// /models > Providers gains no row — it is a setting, not an endpoint.
func TestEnabledSleevIsNotAProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.SetSleev(SleevConfig{Enabled: true}); err != nil {
		t.Fatalf("SetSleev() error = %v", err)
	}

	if _, ok := ResolveProvider(context.Background(), store); ok {
		t.Fatal("ResolveProvider() = ok with only the gateway enabled, want nothing to infer through")
	}
	for _, id := range ProvidersByOrder() {
		if strings.Contains(strings.ToLower(ProviderDisplayName(id)), "sleev") {
			t.Fatalf("ProvidersByOrder() lists %q, want the gateway to stay a setting", id)
		}
		if _, ok := GetMeta(id, AuthAPIKey); ok && strings.EqualFold(string(id), "sleev") {
			t.Fatalf("GetMeta(%q) = registered, want the gateway not to be a provider", id)
		}
	}
}

// ---------------------------------------------------------------------------
// The live check: real providers, the real gateway, a real turn
// ---------------------------------------------------------------------------

// liveSleevEnvVar turns on the check below. It is off by default because it
// needs two things this test cannot start: the Sleev gateway listening on this
// machine, and the vendors the user has connected in their own settings.
//
// Run it by hand after changing anything about routing:
//
//	SAN_SLEEV_LIVE=1 go test ./internal/llm -run SleevLive -v -count=1
const liveSleevEnvVar = "SAN_SLEEV_LIVE"

// liveSleevConfigEnvVar states where the settings to copy live, for a sandbox
// that masks HOME. It defaults to ~/.san.
const liveSleevConfigEnvVar = "SAN_SLEEV_LIVE_CONFIG"

// isTransportFailure reports whether a turn failed to leave San at all, as
// opposed to coming back with an answer from the upstream the gateway reached.
func isTransportFailure(err error, gateway string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	host := strings.TrimPrefix(strings.TrimPrefix(gateway, "http://"), "https://")
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "dial tcp") ||
		strings.Contains(msg, host)
}

// TestSleevLiveGatewayRoutesConfiguredProviders sends one turn per connected
// provider through the gateway the user runs, using a copy of the settings San
// already holds, and reports what came back.
//
// It asserts the two things a fake gateway cannot: that San's request left
// towards the real gateway, and that the gateway forwarded it far enough to
// produce an upstream answer or an upstream error. What that answer says is
// the vendor's business — an exhausted free tier is still a routed request —
// so it is logged rather than asserted.
func TestSleevLiveGatewayRoutesConfiguredProviders(t *testing.T) {
	if os.Getenv(liveSleevEnvVar) == "" {
		t.Skipf("set %s=1 to send real turns through the running Sleev gateway", liveSleevEnvVar)
	}

	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir() error = %v", err)
	}
	// The settings to copy. A sandbox that masks HOME still has them under the
	// user's own config directory, so the path can be stated outright.
	sourceDir := filepath.Join(realHome, confdir.Name)
	if override := os.Getenv(liveSleevConfigEnvVar); override != "" {
		sourceDir = override
	}

	// A copy, so turning the gateway on here never touches the running
	// install's settings.
	home := t.TempDir()
	configDir := filepath.Join(home, confdir.Name)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", configDir, err)
	}
	for _, name := range []string{"providers.json", "secrets.json"} {
		src := filepath.Join(sourceDir, name)
		data, err := os.ReadFile(src)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("reading %s: %v", src, err)
		}
		if err := os.WriteFile(filepath.Join(configDir, name), data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	t.Setenv("HOME", home)
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)

	store, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.SetSleev(SleevConfig{Enabled: true}); err != nil {
		t.Fatalf("SetSleev() error = %v", err)
	}
	gateway := store.Sleev()
	if gateway == nil {
		t.Fatal("Sleev() = nil after enabling, want the stored config")
	}
	probeSleevGateway(t, gateway.BaseURL)

	connections := store.Connections()
	if len(connections) == 0 {
		t.Skip("no provider is connected in this install; connect one before running the live check")
	}

	for name, conn := range connections {
		t.Run(name, func(t *testing.T) {
			provider := ProviderID(name)
			p, err := GetProvider(context.Background(), provider, conn.AuthMethod)
			if err != nil {
				t.Skipf("%s is not a provider this build serves: %v", name, err)
			}

			// The listing is a request of its own, so it proves the gateway
			// answered this provider's GET path before any turn is sent.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			models, err := p.ListModels(ctx)
			if err != nil {
				if isTransportFailure(err, gateway.BaseURL) {
					t.Fatalf("listing models never reached the gateway: %v", err)
				}
				t.Logf("gateway forwarded the model listing; the upstream answered with an error: %v", err)
			}
			if len(models) == 0 {
				t.Skip("no model to ask; the listing above says why")
			}

			// One model is enough to prove the turn path: the listing is
			// ordered the way the vendor orders it.
			model := models[0].ID
			if override := os.Getenv("SAN_SLEEV_LIVE_MODEL"); override != "" && len(models) > 1 {
				model = override
			}
			text, err := sleevTurn(ctx, p, model)
			switch {
			case err != nil && isTransportFailure(err, gateway.BaseURL):
				t.Fatalf("the turn never reached the gateway: %v", err)
			case err != nil:
				// The upstream refused the request — an expired credential, an
				// exhausted free tier — but the gateway did route it, which is
				// what this check is about.
				t.Logf("gateway forwarded the turn; %s answered %q with: %v", model, text, err)
			default:
				t.Logf("routed %s through %s: %q", model, gateway.BaseURL, text)
			}
		})
	}

	// The other half of the switch. The gateway is turned off and its address
	// pointed at a closed port, so a provider that still answers can only have
	// bypassed it: its own host is the one address left to reach.
	if err := store.SetSleev(SleevConfig{Enabled: false, BaseURL: "http://127.0.0.1:1"}); err != nil {
		t.Fatalf("SetSleev(disabled) error = %v", err)
	}
	for name, conn := range connections {
		t.Run("direct/"+name, func(t *testing.T) {
			p, err := GetProvider(context.Background(), ProviderID(name), conn.AuthMethod)
			if err != nil {
				t.Skipf("%s is not a provider this build serves: %v", name, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			models, err := p.ListModels(ctx)
			if err != nil {
				t.Fatalf("listing with the gateway off failed, so the provider did not go direct: %v", err)
			}
			if len(models) == 0 {
				t.Fatal("listing with the gateway off returned no models")
			}
			t.Logf("gateway off: %s listed %d models from its own endpoint", name, len(models))
		})
	}
}

// probeSleevGateway fails unless something answers at the gateway address and
// answers the way Sleev does: a request with no sleev-harness header is refused
// with the header it is missing.
func probeSleevGateway(t *testing.T, baseURL string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/", nil)
	if err != nil {
		t.Fatalf("building the probe request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no Sleev gateway at %s (start it, or correct the stored address): %v", baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("gateway probe = %s, want the 400 Sleev answers with when no harness header is sent", resp.Status)
	}
}

// sleevTurn sends one turn and returns the text it collected along with the
// first error, if the upstream answered with one.
func sleevTurn(ctx context.Context, p Provider, model string) (string, error) {
	var text strings.Builder
	var first error
	for chunk := range legacyStream(ctx, p, CompletionOptions{
		Model:     model,
		Messages:  []core.Message{core.UserMessage("Reply with the word ready.", nil)},
		MaxTokens: 32,
	}) {
		switch chunk.Type {
		case ChunkTypeText:
			text.WriteString(chunk.Text)
		case ChunkTypeError:
			if first == nil && chunk.Error != nil {
				first = chunk.Error
			}
		}
	}
	return strings.TrimSpace(text.String()), first
}
