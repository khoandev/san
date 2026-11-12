// The Sleev gateway: one local forwarder every provider's traffic can be sent
// through.
//
// A user runs Sleev beside San and configures it with the upstream it should
// reach. San's half of that arrangement is small and sits here rather than in
// each vendor: rewrite the address of the client a turn is built from, so its
// requests go to the gateway instead of the vendor. The gateway learns the rest
// from the path, the way Sleev's harness setup pages prescribe it —
//
//	<gateway>/sleev/<harness ID>/<provider ID>     a provider Sleev knows
//	<gateway>/sleev/<harness ID>/<full base URL>   a custom endpoint
//
// Everything else, the protocol and the credential above all, is left exactly
// as the vendor stated it.
package llm

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
)

// sleevProviders maps one of San's vendors to the provider ID the gateway
// knows it by. Sleev lists the IDs per harness (see its Providers page); the
// ones here are the providers those harnesses share, plus San's own OpenCode
// Zen row.
//
// A vendor absent from this table — a custom endpoint above all, or one whose
// Sleev name depends on the plan the connection is on (Alibaba, Z.ai's metered
// API) — is routed by its own base URL instead, the custom-provider form the
// gateway documents. A name only decides which upstream the gateway picks; the
// URL form states it outright.
var sleevProviders = map[string]sleevProvider{
	"anthropic":      {id: "anthropic"},
	"openai":         {id: "openai"},
	"openai-codex":   {id: "codex"},
	"copilot":        {id: "copilot"},
	"google":         {id: "google"},
	"moonshot":       {id: "moonshotai"},
	"mimo":           {id: "mimo"},
	opencodeVendorID: {id: "opencode"},
	// San reaches Z.ai twice through the same vendor row: the metered API and
	// the coding plan. Only the coding plan has a Sleev name, so the auth
	// method is part of the match rather than the vendor ID alone.
	"bigmodel": {id: "zai-coding-plan", auth: AuthCoding},
}

// sleevProvider is one row of sleevProviders: the gateway's ID for the vendor,
// and the auth method it applies to. An empty auth matches every method.
type sleevProvider struct {
	id   string
	auth AuthMethod
}

// sleevDefaultHosts is where each protocol's driver would go on its own, for
// the vendors that state no address of their own.
//
// TODO(sdk-go): the default hosts belong to the drivers, which hold them
// privately; expose them (e.g. ai.API.DefaultBaseURL) and drop them here.
var sleevDefaultHosts = map[ai.API]string{
	ai.APIAnthropicMessages: "https://api.anthropic.com",
	ai.APIOpenAIChat:        "https://api.openai.com/v1",
	ai.APIOpenAIResponses:   "https://api.openai.com/v1",
	ai.APIGoogleGenAI:       "https://generativelanguage.googleapis.com",
}

// sleevGate is one enabled gateway, resolved: where to send a request and on
// behalf of which harness. It holds no credential of its own — the provider's
// key rides along untouched, because the gateway forwards it upstream.
type sleevGate struct {
	baseURL string
	harness string
}

// loadSleevGate returns the gateway to route through, or nil when the user has
// not enabled one. It never fails: a settings file San cannot read is the same
// situation as a gateway that was never configured, and refusing to open every
// provider over it would turn a settings problem into an unusable install.
//
// The settings are read straight from the user's file rather than through a
// Store: the gateway is a user-wide setting, so no project file can state it,
// and routing must not depend on providers.json being readable.
func loadSleevGate() *sleevGate {
	cfg, _ := setting.UserSleev()
	if !cfg.IsEnabled() {
		return nil
	}
	return &sleevGate{
		baseURL: cfg.BaseURL,
		harness: cfg.Harness,
	}
}

// route rewrites the address of one turn's config so the request reaches the
// gateway. A nil gate — the common case — changes nothing.
//
// It is applied to the config a client is built from, not to the endpoint: the
// gateway carries turns, while the metadata calls that open a connection — the
// model listing above all — name no model and are refused there. Leaving the
// endpoint on the vendor's own address is what keeps /models open with the
// gateway on.
//
// The protocol, the credential and the headers are deliberately untouched: the
// gateway forwards what the provider's driver sends, so an Anthropic endpoint
// with an Anthropic key stays exactly that, its own headers still state who is
// calling, and the key reaches the upstream as the provider's own. Only the
// address moves.
func (g *sleevGate) route(cfg *ai.Config, vendor catalog.Vendor, auth AuthMethod) error {
	if g == nil {
		return nil
	}
	target, err := sleevTarget(cfg.Model.BaseURL, cfg.Vertex, vendor, auth)
	if err != nil {
		return err
	}
	cfg.Model.BaseURL = sleevRouteURL(g.baseURL, g.harness, target)
	return nil
}

// sleevRouteURL builds the address the docs prescribe:
// <gateway>/sleev/<harness>/<target>. The target is appended verbatim — a
// provider ID, or a full base URL whose scheme and slashes are part of it —
// because the gateway parses the remainder of the path rather than expecting it
// escaped.
func sleevRouteURL(gateway, harness, target string) string {
	return strings.TrimRight(gateway, "/") + "/sleev/" + harness + "/" + target
}

// sleevTarget is the tail of the gateway's path for one endpoint: the provider
// ID the gateway knows, or the upstream's own base URL when it knows the vendor
// by neither name nor row.
func sleevTarget(upstream string, vertex *ai.VertexConfig, vendor catalog.Vendor, auth AuthMethod) (string, error) {
	// A Vertex deployment's host is computed from its project and region, and
	// names a tenant rather than the product, so there is no single address to
	// hand the gateway.
	if vertex != nil || strings.HasSuffix(vendor.ID, "-vertex") {
		return "", fmt.Errorf(
			"llm: sleev cannot route %q: a Vertex deployment's host is computed from its project and region", vendor.ID)
	}
	if provider, ok := sleevProviders[vendor.ID]; ok && (provider.auth == "" || provider.auth == auth) {
		return provider.id, nil
	}

	host, ok := sleevDefaultHosts[vendor.API]
	if !ok {
		return "", fmt.Errorf(
			"llm: sleev cannot route %q: the gateway speaks the Anthropic, OpenAI and Google protocols, not %q",
			vendor.ID, vendor.API)
	}
	// The custom-provider form: the endpoint's own address when it states one —
	// a custom endpoint, or a vendor San overrides — else the protocol's default
	// host. The trailing slash is trimmed so the gateway's own path is never
	// doubled once it appends the request's.
	target := strings.TrimRight(strings.TrimSpace(upstream), "/")
	if target == "" {
		target = host
	}
	return target, nil
}
