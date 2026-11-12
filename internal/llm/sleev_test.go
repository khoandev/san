package llm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
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
	"github.com/genai-io/san/internal/setting"
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

// Routing moves the address — and only the address. The gateway carries
// whichever protocol and credential the driver sends, and the vendor's own
// headers still state who is calling.
func TestSleevGateRoutesThroughTheGateway(t *testing.T) {
	vendor := mustFindVendor(t, "moonshot")
	cfg := ai.Config{
		APIKey: "sk-upstream",
		Model:  ai.Model{ID: "kimi-k3", BaseURL: vendor.BaseURL, API: vendor.API},
	}

	if err := testSleevGate().route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}

	if want := "http://127.0.0.1:17321/sleev/pi/" + strings.TrimRight(vendor.BaseURL, "/"); cfg.Model.BaseURL != want {
		t.Fatalf("Model.BaseURL = %q, want %q", cfg.Model.BaseURL, want)
	}
	if cfg.APIKey != "sk-upstream" {
		t.Fatalf("APIKey = %q, want the provider's own credential", cfg.APIKey)
	}
	if cfg.Model.API != vendor.API {
		t.Fatalf("Model.API = %q, want the vendor's own protocol %q", cfg.Model.API, vendor.API)
	}
	if cfg.Model.Headers != nil {
		t.Fatalf("Model.Headers = %v, want none: the route is stated in the path, not in headers", cfg.Model.Headers)
	}
}

// The path is the whole contract: <gateway>/sleev/<harness>/<full base URL>.
// There is one form — the custom-provider form Sleev's setup page documents —
// so every provider is named by the address it would have reached directly.
func TestSleevGateBuildsTheDocumentedPath(t *testing.T) {
	piGate := sleevGate{baseURL: "http://127.0.0.1:17321", harness: "pi"}
	opencodeGate := sleevGate{baseURL: "http://127.0.0.1:17321", harness: "opencode"}

	tests := []struct {
		name    string
		gate    sleevGate
		vendor  catalog.Vendor
		baseURL string
		want    string
	}{
		{
			// Sleev's custom-providers page states this one verbatim.
			name:    "a custom endpoint states its own base URL",
			gate:    opencodeGate,
			vendor:  catalog.Vendor{ID: string(CustomProvider), API: ai.APIOpenAIChat},
			baseURL: "https://provider.example.test/v1",
			want:    "http://127.0.0.1:17321/sleev/opencode/https://provider.example.test/v1",
		},
		{
			// The regression the single form fixes: Zen's own address has to
			// survive, instead of a provider name replacing it with another
			// upstream on the same host.
			name:    opencodeVendorID,
			gate:    piGate,
			vendor:  mustFindVendor(t, opencodeVendorID),
			baseURL: opencodeBaseURL,
			want:    "http://127.0.0.1:17321/sleev/pi/https://opencode.ai/zen/v1",
		},
		{
			name:   "anthropic",
			gate:   piGate,
			vendor: mustFindVendor(t, "anthropic"),
			// Anthropic states no address of its own, so the driver's default
			// host is what the path carries.
			want: "http://127.0.0.1:17321/sleev/pi/https://api.anthropic.com",
		},
		{
			name:   "openai",
			gate:   piGate,
			vendor: mustFindVendor(t, "openai"),
			want:   "http://127.0.0.1:17321/sleev/pi/https://api.openai.com/v1",
		},
		{
			name:   "google",
			gate:   piGate,
			vendor: mustFindVendor(t, "google"),
			want:   "http://127.0.0.1:17321/sleev/pi/https://generativelanguage.googleapis.com",
		},
		{
			name:   "moonshot",
			gate:   piGate,
			vendor: mustFindVendor(t, "moonshot"),
			want:   "http://127.0.0.1:17321/sleev/pi/https://api.moonshot.cn/v1",
		},
		{
			name:   "mimo",
			gate:   piGate,
			vendor: mustFindVendor(t, "mimo"),
			want:   "http://127.0.0.1:17321/sleev/pi/https://api.xiaomimimo.com/anthropic",
		},
		{
			name:   "bigmodel",
			gate:   piGate,
			vendor: mustFindVendor(t, "bigmodel"),
			want:   "http://127.0.0.1:17321/sleev/pi/https://open.bigmodel.cn/api/paas/v4",
		},
		{
			// A second harness, on a vendor whose base URL is a bare host:
			// the harness segment comes from the setting, the rest from the
			// endpoint.
			name:    "the harness segment follows the harness setting",
			gate:    opencodeGate,
			vendor:  mustFindVendor(t, opencodeVendorID),
			baseURL: "https://api.openai.com/v1",
			want:    "http://127.0.0.1:17321/sleev/opencode/https://api.openai.com/v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Without an override the endpoint is configured with the vendor's
			// own base URL, which is what a provider factory passes along.
			baseURL := tt.baseURL
			if baseURL == "" {
				baseURL = tt.vendor.BaseURL
			}
			cfg := ai.Config{Model: ai.Model{BaseURL: baseURL}}
			if err := tt.gate.route(&cfg, tt.vendor); err != nil {
				t.Fatalf("route() error = %v", err)
			}
			if cfg.Model.BaseURL != tt.want {
				t.Fatalf("Model.BaseURL = %q, want %q", cfg.Model.BaseURL, tt.want)
			}
		})
	}
}

// An address that arrives with a trailing slash must not produce a doubled
// path once the gateway appends the request's own.
func TestSleevGateTrimsTrailingSlashes(t *testing.T) {
	gate := sleevGate{baseURL: "http://127.0.0.1:17321/", harness: "pi"}
	vendor := catalog.Vendor{ID: string(CustomProvider), API: ai.APIOpenAIChat}
	cfg := ai.Config{Model: ai.Model{BaseURL: " https://llm.example.test/v1// "}}

	if err := gate.route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}
	if want := "http://127.0.0.1:17321/sleev/pi/https://llm.example.test/v1"; cfg.Model.BaseURL != want {
		t.Fatalf("Model.BaseURL = %q, want %q", cfg.Model.BaseURL, want)
	}
}

// A vendor that states no address of its own — the drivers hold the default
// privately — still has to be routed, so the protocol's own host answers for it
// in the URL form.
func TestSleevGateFallsBackToTheProtocolHost(t *testing.T) {
	tests := []struct {
		vendor catalog.Vendor
		want   string
	}{
		{
			vendor: catalog.Vendor{ID: "nowhere-anthropic", API: ai.APIAnthropicMessages},
			want:   "http://127.0.0.1:17321/sleev/pi/https://api.anthropic.com",
		},
		{
			vendor: catalog.Vendor{ID: "nowhere-openai", API: ai.APIOpenAIChat},
			want:   "http://127.0.0.1:17321/sleev/pi/https://api.openai.com/v1",
		},
		{
			vendor: catalog.Vendor{ID: "nowhere-google", API: ai.APIGoogleGenAI},
			want:   "http://127.0.0.1:17321/sleev/pi/https://generativelanguage.googleapis.com",
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.vendor.API), func(t *testing.T) {
			cfg := ai.Config{APIKey: "sk-upstream"}
			if err := testSleevGate().route(&cfg, tt.vendor); err != nil {
				t.Fatalf("route() error = %v", err)
			}
			if cfg.Model.BaseURL != tt.want {
				t.Fatalf("Model.BaseURL = %q, want %q", cfg.Model.BaseURL, tt.want)
			}
		})
	}
}

// A deployment whose address San cannot state must fail loudly: routing it to
// the gateway anyway would send the request somewhere the gateway cannot
// forward, and silently skipping the gateway would hide that sleev is on.
func TestSleevGateRefusesAnUnknownUpstream(t *testing.T) {
	tests := []struct {
		name   string
		vendor catalog.Vendor
		cfg    ai.Config
		want   string
	}{
		{
			// A Vertex deployment's host is computed from its project and
			// region, and the endpoint states no base URL San can put in the
			// gateway's path.
			name:   "vertex deployment",
			vendor: catalog.Vendor{ID: "anthropic-vertex", API: ai.APIAnthropicVertex},
			cfg:    ai.Config{Vertex: &ai.VertexConfig{Project: "a-project"}},
			want:   "anthropic-vertex",
		},
		{
			name:   "google vertex protocol",
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
			if cfg.Model.BaseURL != tt.cfg.Model.BaseURL || cfg.APIKey != "sk-upstream" || cfg.Model.Headers != nil {
				t.Fatalf("refused config was mutated: %#v", cfg)
			}
		})
	}
}

// The common case: no gateway, so the endpoint the factory built stands.
func TestNilSleevGateLeavesTheEndpointAlone(t *testing.T) {
	vendor := mustFindVendor(t, "moonshot")
	cfg := ai.Config{APIKey: "sk-upstream", Model: ai.Model{ID: "kimi-k3", BaseURL: vendor.BaseURL}}

	var gate *sleevGate
	if err := gate.route(&cfg, vendor); err != nil {
		t.Fatalf("route() error = %v", err)
	}
	if cfg.Model.BaseURL != vendor.BaseURL || cfg.APIKey != "sk-upstream" || cfg.Model.Headers != nil {
		t.Fatalf("config changed with no gateway enabled: %#v", cfg)
	}
}

// A listing names no model, so the gateway refuses it: with the gateway on, the
// picker still has to fill. Only the client a turn is built from is routed; the
// endpoint the listing is fetched from keeps the vendor's own address.
//
// Two protocol families are covered because their drivers resolve the request
// path differently: the turn must carry the whole base URL in the gateway's
// path either way.
func TestSleevGateLeavesTheModelListingDirect(t *testing.T) {
	tests := []struct {
		name         string
		vendorID     string
		providerName string
		model        string
		// basePath is the path the endpoint's base URL carries, as real
		// provider addresses do. Empty for Anthropic, whose driver appends
		// /v1 itself.
		basePath string
		listing  string
		stream   string
	}{
		{
			name:         "anthropic messages",
			vendorID:     "anthropic",
			providerName: "anthropic:api_key",
			model:        "claude-opus-5",
			listing:      `{"data":[{"id":"claude-opus-5","type":"model","display_name":"Claude Opus 5"}],"has_more":false}`,
			stream:       "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"model\":\"claude-opus-5\"}}\n\n",
		},
		{
			name:         "openai chat completions",
			vendorID:     opencodeVendorID,
			providerName: opencodeVendorID + ":api_key",
			model:        "beam-free",
			basePath:     "/v1",
			listing:      `{"data":[{"id":"beam-free","type":"model","display_name":"Beam Free"}],"has_more":false}`,
			stream:       "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"beam-free\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			secret.ResetDefault()
			t.Cleanup(secret.ResetDefault)

			vendor := mustFindVendor(t, tt.vendorID)

			var listings, turns int
			var listingPath, turnPath string
			direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				listings++
				listingPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tt.listing)
			}))
			t.Cleanup(direct.Close)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turns++
				turnPath = r.URL.Path
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tt.stream)
			}))
			t.Cleanup(gateway.Close)

			if err := setting.SaveSleev(true, gateway.URL, "pi"); err != nil {
				t.Fatalf("setting.SaveSleev() error = %v", err)
			}

			// The base URL carries a path segment where the real provider's
			// does, so a driver that drops it on the way to the gateway is
			// caught.
			baseURL := direct.URL + tt.basePath
			p := newVendorProvider(tt.providerName, vendor,
				endpoint.Config{APIKey: "test-key", BaseURL: baseURL})

			models, err := p.ListModels(context.Background())
			if err != nil {
				t.Fatalf("ListModels() with the gateway enabled error = %v", err)
			}
			if listings == 0 {
				t.Fatal("the listing never reached the vendor's own address, so /models could not fill")
			}
			if !strings.Contains(listingPath, "/models") {
				t.Fatalf("listing path = %q, want the vendor's own /models", listingPath)
			}
			if turns != 0 {
				t.Fatalf("the listing went to the gateway %d times, want the vendor's own address", turns)
			}
			if len(models) == 0 {
				t.Fatal("ListModels() = nothing, want the vendor's listing")
			}

			// The turn is the one call that takes the gateway.
			for range legacyStream(context.Background(), p, CompletionOptions{
				Model:     tt.model,
				Messages:  []core.Message{core.UserMessage("hi", nil)},
				MaxTokens: 8,
			}) {
			}
			if turns == 0 {
				t.Fatal("the turn never reached the gateway")
			}
			// The path names the endpoint's own base URL, which is what the
			// gateway forwards to: no provider name is consulted, so the turn
			// cannot be rewritten to a different upstream.
			t.Logf("turn path = %q", turnPath)
			if want := "/sleev/pi/" + baseURL; !strings.HasPrefix(turnPath, want) {
				t.Fatalf("turn path = %q, want it to start with %q", turnPath, want)
			}
		})
	}
}

// The gate is read from the user's settings at the moment a provider is opened,
// which is what makes a settings change take effect without a restart — and
// what keeps the route out of reach of a project's providers.json.
func TestLoadSleevGateReadsTheSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	secret.ResetDefault()
	t.Cleanup(secret.ResetDefault)

	if gate := loadSleevGate(); gate != nil {
		t.Fatalf("loadSleevGate() = %#v, want nil before the user configures one", gate)
	}

	if err := setting.SaveSleev(true, "http://127.0.0.1:9999", ""); err != nil {
		t.Fatalf("setting.SaveSleev() error = %v", err)
	}

	gate := loadSleevGate()
	if gate == nil {
		t.Fatal("loadSleevGate() = nil, want the enabled gateway")
	}
	if gate.baseURL != "http://127.0.0.1:9999" {
		t.Fatalf("baseURL = %q, want the stored address", gate.baseURL)
	}
	if gate.harness != setting.DefaultSleevHarness {
		t.Fatalf("harness = %q, want %q", gate.harness, setting.DefaultSleevHarness)
	}

	if err := setting.SaveSleev(false, "http://127.0.0.1:9999", ""); err != nil {
		t.Fatalf("setting.SaveSleev(disabled) error = %v", err)
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
	if err := setting.SaveSleev(true, "", ""); err != nil {
		t.Fatalf("setting.SaveSleev() error = %v", err)
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

// liveSleevModelEnvVar names the model one scoped check must prove. With it
// set, an empty answer is a failure rather than a note: naming the model is
// what turns the check into a proof that the gateway reaches it.
const liveSleevModelEnvVar = "SAN_SLEEV_LIVE_MODEL"

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
	if err := setting.SaveSleev(true, "", ""); err != nil {
		t.Fatalf("setting.SaveSleev() error = %v", err)
	}
	gateway, ok := setting.UserSleev()
	if !ok {
		t.Fatal("UserSleev() = not found after enabling, want the stored config")
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

			// The listing is the half that must not travel: it names no model,
			// so the gateway refuses it, and /models still has to fill with the
			// gateway enabled. A list here is that contract holding.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			models, err := p.ListModels(ctx)
			if err != nil {
				t.Fatalf("listing models failed with the gateway enabled, so /models could not fill: %v", err)
			}
			if len(models) == 0 {
				t.Skip("no model to ask; the listing above says why")
			}

			// One model is enough to prove the turn path. It has to be one a
			// turn can answer with: a listing mixes chat models with image and
			// embedding ones, and a turn sent to those comes back 400 without
			// ever testing the gateway.
			model := liveChatModel(models)
			override := os.Getenv(liveSleevModelEnvVar)
			if override != "" {
				model = override
			}
			text, err := sleevTurn(ctx, p, model, liveTurnTokens)
			switch {
			case err != nil && isTransportFailure(err, gateway.BaseURL):
				t.Fatalf("the turn never reached the gateway: %v", err)
			case err != nil && override != "":
				// Naming a model makes this a proof about that model, so an
				// error is not a routed-but-refused note: nothing was proven.
				t.Fatalf("%s did not answer through the gateway: %v", model, err)
			case err != nil:
				// The upstream refused the request — an expired credential, an
				// exhausted free tier — but the gateway did route it, which is
				// what this check is about.
				t.Logf("gateway forwarded the turn; %s answered %q with: %v", model, text, err)
			default:
				t.Logf("routed %s through %s: %q", model, gateway.BaseURL, text)
				if override != "" && text == "" {
					t.Fatalf("%s answered with no text through the gateway, so the route is not proven", model)
				}
			}
		})
	}

	// The other half of the switch. The gateway is turned off and its address
	// pointed at a closed port, so a provider that still answers can only have
	// bypassed it: its own host is the one address left to reach.
	if err := setting.SaveSleev(false, "http://127.0.0.1:1", ""); err != nil {
		t.Fatalf("setting.SaveSleev(disabled) error = %v", err)
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
// liveChatModel picks the first model in a listing a conversation can use.
// Listings put image, embedding and speech models in the same list as chat
// ones, and a turn sent to one of those is refused upstream — which would make
// the live check pass on a request that could never have produced an answer.
func liveChatModel(models []ModelInfo) string {
	for _, m := range models {
		id := strings.ToLower(m.ID)
		switch {
		case strings.Contains(id, "image"),
			strings.Contains(id, "embed"),
			strings.Contains(id, "whisper"),
			strings.Contains(id, "tts"):
			continue
		}
		return m.ID
	}
	return models[0].ID
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

// liveTurnTokens is the budget of one live turn. Reasoning models spend the
// budget before writing text, so a small cap makes a working route look like an
// empty answer.
const liveTurnTokens = 1024

// sleevTurn sends one turn and returns the text it collected along with the
// first error, if the upstream answered with one.
func sleevTurn(ctx context.Context, p Provider, model string, maxTokens int) (string, error) {
	var text strings.Builder
	var first error
	for chunk := range legacyStream(ctx, p, CompletionOptions{
		Model:     model,
		Messages:  []core.Message{core.UserMessage("Reply with the word ready.", nil)},
		MaxTokens: maxTokens,
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
