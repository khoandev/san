package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	utls "github.com/refraction-networking/utls"
	"github.com/genai-io/sdk-go/pkg/ai/catalog"
	"github.com/genai-io/sdk-go/pkg/ai/endpoint"
)

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
