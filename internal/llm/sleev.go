// The Sleev gateway: one local forwarder every provider's traffic can be sent
// through.
//
// A user runs Sleev beside San and configures it with the upstream it should
// reach. San's half of that arrangement is small and sits here rather than in
// each vendor: rewrite the endpoint the factory just built so its requests go
// to the gateway instead of the vendor, and say — in the three headers the
// gateway reads — which family the request belongs to, where to forward it,
// and which client is asking. Everything else, the protocol above all, is left
// exactly as the vendor stated it.
package llm

import (
	"fmt"
	"maps"
	"strings"

	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"
)

// The headers the gateway reads: which provider the request belongs to, where
// to forward it, and which harness is asking. The provider is not derivable by
// the gateway from the body, and it refuses a request that names none, so it
// is the one header routing cannot leave out.
const (
	sleevProviderHeader = "sleev-provider"
	sleevHarnessHeader  = "sleev-harness"
	sleevBaseURLHeader  = "sleev-base-url"
)

// sleevUpstreams is how one of San's protocols is reached through the gateway:
// the family it routes as, and where that family's driver would go on its own.
//
// TODO(sdk-go): the default hosts belong to the drivers, which hold them
// privately; expose them (e.g. ai.API.DefaultBaseURL) and drop them here.
var sleevUpstreams = map[ai.API]struct {
	provider       string
	defaultBaseURL string
}{
	ai.APIAnthropicMessages: {provider: "anthropic", defaultBaseURL: "https://api.anthropic.com"},
	ai.APIOpenAIChat:        {provider: "openai", defaultBaseURL: "https://api.openai.com/v1"},
	ai.APIOpenAIResponses:   {provider: "openai", defaultBaseURL: "https://api.openai.com/v1"},
	ai.APIGoogleGenAI:       {provider: "google", defaultBaseURL: "https://generativelanguage.googleapis.com"},
}

// sleevNamedProviders names the vendors the gateway knows by their own ID
// rather than by the protocol family they speak. OpenCode Zen is the case that
// matters: the gateway serves several OpenAI-speaking upstreams, and only the
// name tells it which one is asking. Moonshot and MiMo are named for the same
// reason — the gateway meters them, not OpenAI.
//
// A vendor absent here — a custom endpoint above all, or one whose Sleev name
// depends on the plan the connection is on (Alibaba, Z.ai) — routes as its
// family and states its own base URL in sleev-base-url, which is what
// actually decides where the gateway forwards: it honours that header even for
// a named provider, and the name only labels the request.
var sleevNamedProviders = map[string]string{
	opencodeVendorID: "opencode",
	"moonshot":       "moonshotai",
	"mimo":           "mimo",
}

// sleevGate is one enabled gateway, resolved: where to send a request and on
// behalf of which harness. It holds no credential of its own — the provider's
// key rides along untouched, because the gateway forwards it upstream.
type sleevGate struct {
	baseURL string
	harness string
}

// loadSleevGate returns the gateway to route through, or nil when the user has
// not enabled one. It never fails: a store San cannot read is the same
// situation as a gateway that was never configured, and refusing to open every
// provider over it would turn a settings problem into an unusable install.
func loadSleevGate() *sleevGate {
	store, err := NewStore()
	if err != nil {
		return nil
	}
	cfg := store.Sleev()
	if !cfg.IsEnabled() {
		return nil
	}
	return &sleevGate{
		baseURL: cfg.BaseURL,
		harness: cfg.Harness,
	}
}

// route rewrites one vendor's endpoint config so its requests reach the
// gateway. A nil gate — the common case — changes nothing.
//
// The protocol and the credential are deliberately untouched: the gateway
// forwards what the provider's driver sends, so an Anthropic endpoint with an
// Anthropic key stays exactly that, and the key reaches the upstream as the
// provider's own. Only the address and the three sleev headers move.
func (g *sleevGate) route(cfg *endpoint.Config, vendor catalog.Vendor) error {
	if g == nil {
		return nil
	}
	provider, upstream, err := sleevTarget(cfg, vendor)
	if err != nil {
		return err
	}

	// The vendor's own headers are the identity it expects the gateway to
	// forward — OpenCode Zen's session markers, for one. endpoint.New only
	// adopts them when the caller states none, so they are seeded here
	// rather than replaced: routing must add headers, never drop them.
	headers := maps.Clone(vendor.Headers)
	if headers == nil {
		headers = make(map[string]string, len(cfg.Headers)+4)
	}
	maps.Copy(headers, cfg.Headers)
	headers[sleevProviderHeader] = provider
	headers[sleevHarnessHeader] = g.harness
	headers[sleevBaseURLHeader] = upstream

	cfg.BaseURL = g.baseURL
	cfg.Headers = headers
	return nil
}

// sleevTarget is what the gateway is told about one endpoint: which provider
// it belongs to, and the base URL to forward to.
func sleevTarget(cfg *endpoint.Config, vendor catalog.Vendor) (provider, upstream string, err error) {
	// A Vertex deployment's host is computed from its project and region, and
	// names a tenant rather than the product, so there is no single address to
	// hand the gateway.
	if cfg.Vertex != nil {
		return "", "", fmt.Errorf(
			"llm: sleev cannot route %q: a Vertex deployment's host is computed from its project and region", vendor.ID)
	}
	family, ok := sleevUpstreams[vendor.API]
	if !ok {
		return "", "", fmt.Errorf(
			"llm: sleev cannot route %q: the gateway speaks the Anthropic, OpenAI and Google protocols, not %q",
			vendor.ID, vendor.API)
	}
	provider = sleevNamedProviders[vendor.ID]
	if provider == "" {
		provider = family.provider
	}
	// The address the gateway forwards to: the endpoint's own when it states
	// one — a custom endpoint, or a vendor San overrides — else the family's
	// default host. The gateway honours it for a named provider too, which is
	// what lets one code path serve both.
	upstream = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if upstream == "" {
		upstream = family.defaultBaseURL
	}
	return provider, upstream, nil
}
