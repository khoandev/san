package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"

	"github.com/genai-io/san/internal/secret"

	// The wire protocols San's vendors speak, and the two Vertex deployments of them.
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/anthropic/vertex"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/google"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/google/vertex"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/chat"
	_ "github.com/genai-io/sdk-go/pkg/ai/driver/openai/responses"
)

// Which San provider is which catalog vendor.
//
// San names a connection by provider *and* auth method — "anthropic:vertex" is
// the same models reached a different way — while the SDK's catalog gives each
// way of reaching an endpoint its own vendor row. The table below is that
// mapping, and it is all there is to adding a vendor: no package, no client, no
// conversion code.

const (
	// opencodeVendorID is the vendor key for OpenCode's gateway. The SDK's
	// catalog has no row for it, so San supplies the row itself; the ID is
	// still the one the model data is keyed by.
	opencodeVendorID = "opencode"
	// opencodeBaseURL is the Zen gateway, which is OpenAI Chat Completions.
	opencodeBaseURL = "https://opencode.ai/zen/v1"
)

// entry is one San provider/auth-method pair served by one catalog vendor.
type vendorEntry struct {
	meta     Meta
	vendorID string

	// build states the vendor row outright, for the endpoints the catalog has
	// no row for and whose address lives in San's store rather than in a
	// constant. When set it takes the place of vendorID.
	build func() (catalog.Vendor, error)

	// configure fills in what the catalog cannot: a credential from San's
	// secret store, an endpoint the user set, a deployment. Nil means the
	// common case — the vendor's first key variable and its default host.
	configure func(catalog.Vendor, *endpoint.Config) error
}

// displays are the provider-level UI rows, one per San provider.
var vendorDisplays = map[ProviderID]ProviderDisplay{
	Anthropic:      {Name: "Anthropic", Order: 10},
	OpenAI:         {Name: "OpenAI", Order: 20},
	Copilot:        {Name: "GitHub Copilot", Order: 25},
	Google:         {Name: "Google", Order: 30},
	DeepSeek:       {Name: "DeepSeek", Order: 40},
	SenseNova:      {Name: "SenseNova", Order: 50},
	MinMax:         {Name: "MiniMax", Order: 60},
	Moonshot:       {Name: "Moonshot", Order: 70},
	Alibaba:        {Name: "Alibaba", Order: 80},
	BigModel:       {Name: "Z.ai (GLM series)", Order: 90},
	Ollama:         {Name: "Ollama (Local)", Order: 100},
	Mimo:           {Name: "Xiaomi MiMo", Order: 110},
	Volcengine:     {Name: "Volcengine Ark", Order: 120},
	AgnesAI:        {Name: "Agnes-AI", Order: 130},
	CustomProvider: {Name: "Custom", Order: 140},
	OpenCodeZen:    {Name: "OpenCode Zen (Free)", Order: 15},
}

var vendorEntries = []vendorEntry{
	{
		meta:     Meta{Provider: Anthropic, AuthMethod: AuthAPIKey, EnvVars: []string{"ANTHROPIC_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "anthropic",
	},
	{
		// A connection requires the project alone: the region is optional,
		// defaults to global, and is still read when set.
		meta: Meta{
			Provider: Anthropic, AuthMethod: AuthVertex, DisplayName: "Vertex AI",
			EnvVars:         []string{"ANTHROPIC_VERTEX_PROJECT_ID"},
			OptionalEnvVars: []string{"CLOUD_ML_REGION"},
			Hint:            vertexHint,
		},
		vendorID: "anthropic-vertex",
	},
	{
		meta:     Meta{Provider: OpenAI, AuthMethod: AuthAPIKey, EnvVars: []string{"OPENAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "openai",
	},
	{
		meta:      Meta{Provider: OpenAI, AuthMethod: AuthSubscription, DisplayName: "ChatGPT Subscription"},
		vendorID:  "openai-codex",
		configure: configureSignIn,
	},
	{
		meta:      Meta{Provider: Copilot, AuthMethod: AuthSubscription, DisplayName: "Copilot Subscription"},
		vendorID:  "copilot",
		configure: configureSignIn,
	},
	{
		meta:     Meta{Provider: Google, AuthMethod: AuthAPIKey, EnvVars: []string{"GOOGLE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "google",
	},
	{
		meta: Meta{
			Provider: Google, AuthMethod: AuthVertex, DisplayName: "Vertex AI",
			EnvVars:         []string{"GOOGLE_CLOUD_PROJECT"},
			OptionalEnvVars: []string{"GOOGLE_CLOUD_LOCATION"},
			Hint:            vertexHint,
		},
		vendorID: "google-vertex",
	},
	{
		meta:     Meta{Provider: DeepSeek, AuthMethod: AuthAPIKey, EnvVars: []string{"DEEPSEEK_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "deepseek",
	},
	{
		meta:     Meta{Provider: SenseNova, AuthMethod: AuthAPIKey, EnvVars: []string{"SENSENOVA_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID: "sensenova",
	},
	{
		meta:     Meta{Provider: MinMax, AuthMethod: AuthAPIKey, EnvVars: []string{"MINIMAX_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "minmax",
	},
	{
		meta:     Meta{Provider: Moonshot, AuthMethod: AuthAPIKey, EnvVars: []string{"MOONSHOT_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "moonshot",
	},
	{
		meta:     Meta{Provider: Alibaba, AuthMethod: AuthAPIKey, EnvVars: []string{"DASHSCOPE_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "alibaba",
	},
	{
		meta:     Meta{Provider: BigModel, AuthMethod: AuthAPIKey, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "bigmodel",
	},
	{
		meta:      Meta{Provider: BigModel, AuthMethod: AuthCoding, EnvVars: []string{"BIGMODEL_API_KEY"}, DisplayName: "Coding Plan"},
		vendorID:  "bigmodel",
		configure: configureBigModelCoding,
	},
	{
		meta:     Meta{Provider: Ollama, AuthMethod: AuthAPIKey, EnvVars: []string{"OLLAMA_BASE_URL"}, DisplayName: "Local (Ollama)"},
		vendorID: "ollama",
	},
	{
		meta:     Meta{Provider: Mimo, AuthMethod: AuthAPIKey, EnvVars: []string{"MIMO_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "mimo",
	},
	{
		meta:      Meta{Provider: Volcengine, AuthMethod: AuthAPIKey, EnvVars: []string{"VOLCENGINE_API_KEY"}, DisplayName: "Bearer Token API"},
		vendorID:  "volcengine",
		configure: configureVolcengine,
	},
	{
		meta:     Meta{Provider: AgnesAI, AuthMethod: AuthAPIKey, EnvVars: []string{"AGNESAI_API_KEY"}, DisplayName: "Direct API"},
		vendorID: "agnesai",
	},
	{
		// Zen's free tier is served by a host the SDK's catalog has no row for,
		// so the row is built here — see openCodeZenVendor. Its lineups are
		// free models only, which newVendorProvider enforces on both the seed
		// and the live listing.
		meta:      Meta{Provider: OpenCodeZen, AuthMethod: AuthAPIKey, EnvVars: []string{"OPENCODE_API_KEY"}, DisplayName: "Free models"},
		vendorID:  opencodeVendorID,
		configure: configureOpenCodeZen,
	},
	{
		meta:      Meta{Provider: CustomProvider, AuthMethod: AuthAPIKey, EnvVars: []string{CustomAPIKeyEnvVar}, DisplayName: "Direct API"},
		configure: configureCustom,
	},
}

// init makes every vendor in the table reachable through the registry above.
// Importing this package is enough; there is nothing to switch on.
func init() { registerVendors() }

func registerVendors() {
	for name, display := range vendorDisplays {
		RegisterProviderDisplay(name, display)
	}
	for _, e := range vendorEntries {
		Register(e.meta, e.factory())
		// A subscription or a coding plan is a flat fee: pricing its tokens
		// at the metered rate would invent a bill.
		if e.vendorID != "" && (e.meta.AuthMethod == AuthAPIKey || e.meta.AuthMethod == AuthVertex) {
			RegisterCostEstimator(e.meta.Provider, e.meta.AuthMethod, costEstimator(e.vendorID))
		}
	}
	registerAuthenticators()
}

// factory returns the constructor San's registry calls to open this endpoint.
func (e vendorEntry) factory() Factory {
	return func(context.Context) (Provider, error) {
		vendor, err := e.resolveVendor()
		if err != nil {
			return nil, err
		}

		// A missing key is not fatal: the custom host may take none, and the
		// vendor refuses the first request otherwise.
		key, _ := vendor.KeyFromEnv(secret.Resolve)
		baseURL, err := vendor.BaseURLFromEnv(secret.Resolve)
		if err != nil {
			return nil, fmt.Errorf("llm: %w", err)
		}
		// A Vertex row has no key: the driver signs in with Google
		// Application Default Credentials, and the project and region are
		// read through San's secret store. Nil for every other row.
		vertex, err := vendor.VertexFromEnv(secret.Resolve)
		if err != nil {
			return nil, fmt.Errorf("llm: %w", err)
		}
		cfg := endpoint.Config{APIKey: key, BaseURL: baseURL, Vertex: vertex}
		if e.configure != nil {
			if err := e.configure(vendor, &cfg); err != nil {
				return nil, err
			}
		}
		// An enabled gateway takes the endpoint over: every model this vendor
		// serves is reached through it, with the vendor's own credential.
		if err := loadSleevGate().route(&cfg, vendor); err != nil {
			return nil, err
		}
		return newVendorProvider(providerName(e.meta), vendor, cfg), nil
	}
}

// resolveVendor returns the catalog row this entry serves. An entry with no
// vendor ID builds its own, which is how the user-defined endpoint — a host
// that exists in no catalog — reaches the same code path as every other.
func (e vendorEntry) resolveVendor() (catalog.Vendor, error) {
	if e.build != nil {
		return e.build()
	}
	if e.vendorID == "" {
		return customVendor()
	}
	vendor, ok := findVendor(e.vendorID)
	if !ok {
		return catalog.Vendor{}, fmt.Errorf("llm: no catalog vendor %q", e.vendorID)
	}
	return vendor, nil
}

// findVendor returns the row a vendor ID names: the SDK catalog's, or the one
// San supplies for a host the catalog has no entry for.
func findVendor(id string) (catalog.Vendor, bool) {
	if vendor, ok := catalog.Find(id); ok {
		return vendor, true
	}
	if id == opencodeVendorID {
		return openCodeZenVendor(), true
	}
	return catalog.Vendor{}, false
}

// providerName is San's provider identity for a registration, "vendor:auth".
func providerName(meta Meta) string {
	return string(meta.Provider) + ":" + string(meta.AuthMethod)
}

// ---------------------------------------------------------------------------
// The entries that need more than a key and a host
// ---------------------------------------------------------------------------

// vertexHint is what the credential form says above a Vertex deployment: the
// rows name a project and a region, and the credential comes from elsewhere.
const vertexHint = "Signs in with Google Application Default Credentials — run `gcloud auth application-default login` first. Region defaults to global."

// configureBigModelCoding points Z.ai's GLM models at the Coding Plan path,
// which is the same endpoint under a different prefix.
func configureBigModelCoding(_ catalog.Vendor, cfg *endpoint.Config) error {
	cfg.BaseURL = secret.Resolve("BIGMODEL_CODING_BASE_URL")
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://open.bigmodel.cn/api/coding/paas/v4"
	}
	return nil
}

// VolcengineModelEnvVar names the Ark model this account is provisioned for.
//
// Ark serves models through per-account endpoints, so there is no catalog to
// list and its own listing answers for the account rather than for the
// product. Naming the one model is how an Ark user says what they have.
const VolcengineModelEnvVar = "VOLCENGINE_MODEL"

// configureVolcengine seeds the endpoint with the model this account names, so
// the picker has something to show even when Ark's listing says nothing.
func configureVolcengine(vendor catalog.Vendor, cfg *endpoint.Config) error {
	modelID := secret.Resolve(VolcengineModelEnvVar)
	if modelID == "" {
		return nil
	}
	cfg.Models = []ai.Model{newVendorModels(vendor).model(modelID)}
	return nil
}

// customVendor builds a catalog row for the OpenAI-compatible endpoint the
// user configured in the app. It exists in no catalog, so San supplies what a
// vendor entry would have said: the protocol, the host, and nothing else.
func customVendor() (catalog.Vendor, error) {
	store, err := NewStore()
	if err != nil {
		return catalog.Vendor{}, fmt.Errorf("llm: loading the provider store: %w", err)
	}
	cfg := store.CustomProvider()
	if cfg == nil || cfg.BaseURL == "" {
		return catalog.Vendor{}, fmt.Errorf("custom provider not configured: set a base URL under /models > Providers > Custom")
	}
	return catalog.Vendor{
		ID:          string(CustomProvider),
		DisplayName: "Custom",
		API:         ai.APIOpenAIChat,
		BaseURL:     cfg.BaseURL,
		KeyEnv:      []string{CustomAPIKeyEnvVar},
		Compat:      ai.OpenAIChatCompat{},
	}, nil
}

// configureCustom is a no-op beyond what the vendor row already carries: the
// host came from the store and the key from the secret store.
func configureCustom(catalog.Vendor, *endpoint.Config) error { return nil }

// openCodeZenVendor builds the row for OpenCode's Zen gateway, which the SDK's
// catalog does not carry.
//
// It speaks OpenAI Chat Completions, reasons through reasoning_effort, and
// echoes reasoning_content on every assistant message — Zen refuses a thinking
// request whose history omits it. The fixed headers are the fallback identity:
// a call that reaches the endpoint outside any conversation (a model listing, a
// usage poll) still has to look like the OpenCode client it claims to be.
func openCodeZenVendor() catalog.Vendor {
	return catalog.Vendor{
		ID:          opencodeVendorID,
		DisplayName: "OpenCode Zen",
		API:         ai.APIOpenAIChat,
		BaseURL:     opencodeBaseURL,
		BaseURLEnv:  "OPENCODE_ZEN_BASE_URL",
		KeyEnv:      []string{"OPENCODE_API_KEY"},
		Compat: ai.OpenAIChatCompat{
			Thinking:         ai.ThinkingEffort,
			ReasoningContent: true,
		},
		Headers: opencodeFixedHeaders(),
	}
}

// configureOpenCodeZen fills in the credential the free tier expects.
//
// Zen reads a key only for billed models, and its own spelling for "no
// account" is the literal "public". Without it the request goes out with no
// Authorization header at all and is refused before the tier gate runs.
func configureOpenCodeZen(_ catalog.Vendor, cfg *endpoint.Config) error {
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = opencodePublicKey
	}
	// The free tier is gated on the client's TLS handshake, not on anything
	// the request says, so this vendor brings its own client.
	cfg.HTTPClient = opencodeHTTPClient()
	return nil
}

// costEstimator prices a turn from the model's rate card in San's data. A
// model with no card reports unknown, which San renders as "--" rather than as
// free.
func costEstimator(vendorID string) CostEstimator {
	return func(modelID string, usage Usage) (Money, bool) {
		vendor, ok := findVendor(vendorID)
		if !ok {
			return Money{}, false
		}
		pricing := newVendorModels(vendor).model(modelID).Pricing
		if !pricing.Known() {
			return Money{}, false
		}
		cost := pricing.Cost(usage)
		return Money{Amount: cost.Total, Currency: Currency(cost.Currency)}, true
	}
}
