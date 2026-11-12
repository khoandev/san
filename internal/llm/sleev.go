// The Sleev gateway: one local forwarder every provider's traffic can be sent
// through.
//
// A user runs Sleev beside San and configures it with the upstream it should
// reach. San's half of that arrangement is small and sits here rather than in
// each vendor: rewrite the address of the client a turn is built from, so its
// requests go to the gateway instead of the vendor. The gateway learns the rest
// from the path, in the one form its custom-provider setup documents:
//
//	<gateway>/sleev/<harness ID>/<full API base URL>
//
// Every provider takes that form. A vendor Sleev has a name for is not special
// here: its API base URL is the address the gateway would forward to anyway,
// and stating it outright keeps one rule instead of a table that has to list
// every vendor, match its auth plan and go stale when Sleev renames one. Only
// the address moves; the protocol and the credential above all are left exactly
// as the vendor stated them.
package llm

import (
	"fmt"
	"strings"

	"github.com/genai-io/san/internal/setting"
	"github.com/genai-io/sdk-go/pkg/ai"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
)

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
func (g *sleevGate) route(cfg *ai.Config, vendor catalog.Vendor) error {
	if g == nil {
		return nil
	}
	target, err := sleevTarget(cfg.Model.BaseURL, vendor)
	if err != nil {
		return err
	}
	cfg.Model.BaseURL = sleevRouteURL(g.baseURL, g.harness, target)
	return nil
}

// sleevRouteURL builds the address the docs prescribe:
// <gateway>/sleev/<harness>/<base URL>. The target is appended verbatim — its
// scheme and slashes are part of it — because the gateway parses the remainder
// of the path rather than expecting it escaped.
func sleevRouteURL(gateway, harness, target string) string {
	return strings.TrimRight(gateway, "/") + "/sleev/" + harness + "/" + target
}

// sleevTarget is the tail of the gateway's path: the endpoint's own API base
// URL, trailing slash trimmed so the gateway's own path is never doubled once
// it appends the request's.
//
// A vendor that states no address of its own — the drivers hold the protocol's
// default privately — is named by that default, which is the address the driver
// would have reached without the gateway.
func sleevTarget(upstream string, vendor catalog.Vendor) (string, error) {
	if target := strings.TrimRight(strings.TrimSpace(upstream), "/"); target != "" {
		return target, nil
	}
	host, ok := sleevDefaultHosts[vendor.API]
	if !ok {
		return "", fmt.Errorf(
			"llm: sleev cannot route %q: it states no base URL and the gateway speaks the Anthropic, OpenAI and Google protocols, not %q",
			vendor.ID, vendor.API)
	}
	return host, nil
}
