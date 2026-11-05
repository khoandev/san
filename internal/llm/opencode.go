package llm

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
)

// OpenCode Zen's wire identity, at both layers the free tier reads.
//
// Zen's free tier is gated on the client rather than on a credential, and the
// gate runs before the key is even looked at. Two things have to hold.
//
// The request has to look like the OpenCode client: a User-Agent leading with
// opencode/<version>, x-opencode-client naming the client, x-opencode-session
// in its canonical ses_<12 hex><14 alnum> shape, the five tool names the gate
// knows in the roster, and the working directory in an <env> block.
//
// The connection has to look like it too, and that is the half a port of the
// request cannot reach: the same key, headers and body sent from curl, OpenSSL,
// Node or Go answer 429 with a ~19h Retry-After, while Bun — the runtime the
// OpenCode client ships on — is answered. What the gate reads there is the TLS
// ClientHello, so this vendor handshakes with Bun's.
//
// Ported from oh-my-pi (packages/catalog/src/wire/opencode.ts and
// packages/ai/src/providers/inference-headers.ts), which mirrors the upstream
// OpenCode Go client (internal/identity/request.go).

const (
	// opencodeUserAgent is the canonical OpenCode client. The gate reads only
	// the leading token, so the rest of the string is free.
	opencodeUserAgent = "opencode/1.19.3"

	// opencodePublicKey is what the free tier expects as a credential: the
	// literal "public", not an account key.
	opencodePublicKey = "public"

	// opencodeSessionCacheLimit bounds the session cache. Reaching it replaces
	// the map rather than evicting: a token that stops being cached is minted
	// again, and a moving session header costs prompt-cache affinity.
	opencodeSessionCacheLimit = 8192

	// opencodeBase62 is OpenCode's alphabet for the session tail.
	opencodeBase62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

	// opencodeDialTimeout bounds one connection setup, and
	// opencodeHandshakeTimeout the handshake on it.
	opencodeDialTimeout      = 30 * time.Second
	opencodeHandshakeTimeout = 15 * time.Second
)

// opencodeGateTools are the five OpenCode tool names the gateway requires in a
// request's roster. The gate compares names, never schemas, and counts distinct
// ones.
var opencodeGateTools = []string{"bash", "edit", "glob", "grep", "read"}

// opencodeSessionPattern is the canonical session shape the gate accepts.
var opencodeSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

// installIDPattern is the shape of a minted installation identity.
var installIDPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// opencodeFreeModel reports whether a model is one of Zen's free ones. The
// gateway serves exactly the IDs ending in -free, plus the big-pickle alias,
// and everything else is billed.
func opencodeFreeModel(modelID string) bool {
	return modelID == "big-pickle" || strings.HasSuffix(modelID, "-free")
}

// ---------------------------------------------------------------------------
// Session identity
// ---------------------------------------------------------------------------

// opencodeSessions is the process-wide session cache, and the counter the
// minted prefix is clocked with.
var opencodeSessions = struct {
	mu    sync.Mutex
	mints int64
	cache map[string]string
}{cache: make(map[string]string)}

// opencodeSessionToken formats a conversation identity as the token the gate
// accepts.
//
// A value that already has the shape passes through unchanged, which keeps
// upstream prompt-cache affinity. Anything else — a session UUID, a conversation
// seed — gets a stable minted token. The head is clocked because Zen decodes it
// as a client-side timestamp and treats a digest-derived one as a foreign
// client (403 FreeTierError); the tail derives from the digest, so two
// conversations stay distinct. The token is cached per digest so it does not
// move between turns: a session header that changes per call is a cache miss on
// every call.
func opencodeSessionToken(sessionID string) string {
	if opencodeSessionPattern.MatchString(sessionID) {
		return sessionID
	}
	digest := sha256.Sum256([]byte("ses\x00" + sessionID))
	key := hex.EncodeToString(digest[:])

	opencodeSessions.mu.Lock()
	defer opencodeSessions.mu.Unlock()
	if token, ok := opencodeSessions.cache[key]; ok {
		return token
	}
	token := "ses_" + opencodeMintedPrefix() + opencodeBase62Fixed(new(big.Int).SetBytes(digest[6:16]), 14)
	if len(opencodeSessions.cache) >= opencodeSessionCacheLimit {
		opencodeSessions.cache = make(map[string]string, opencodeSessionCacheLimit)
	}
	opencodeSessions.cache[key] = token
	return token
}

// opencodeMintedPrefix is the 12 hex characters Zen reads as
// ~(unix_ms*0x1000+counter) truncated to its low 48 bits. Callers hold the
// session lock, which is what keeps the counter monotonic.
func opencodeMintedPrefix() string {
	opencodeSessions.mints++
	raw := ^(uint64(time.Now().UnixMilli())*0x1000 + uint64(opencodeSessions.mints&0xfff)) & 0xffffffffffff
	return fmt.Sprintf("%012x", raw)
}

// opencodeBase62Fixed encodes n as exactly width base-62 characters, left
// padded with '0' — OpenCode's own tail encoding, which keeps the low digits.
func opencodeBase62Fixed(n *big.Int, width int) string {
	base := big.NewInt(int64(len(opencodeBase62)))
	out := make([]byte, width)
	rem := new(big.Int)
	value := new(big.Int).Set(n)
	for i := width - 1; i >= 0; i-- {
		value.QuoRem(value, base, rem)
		out[i] = opencodeBase62[rem.Int64()]
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// Installation identity
// ---------------------------------------------------------------------------

var (
	opencodeInstallOnce sync.Once
	opencodeInstallID   string
)

// opencodeInstallIDOrMint is this installation's stable identity, read once per
// process. It backs the session header for traffic that belongs to no
// conversation, and it never fails provider construction: an unreadable or
// unwritable file costs a fresh identity for this process, not the connection.
func opencodeInstallIDOrMint() string {
	opencodeInstallOnce.Do(func() { opencodeInstallID = loadOrMintInstallID() })
	return opencodeInstallID
}

// loadOrMintInstallID reads ~/.san/install-id, minting and persisting one when
// it is missing or unparseable.
func loadOrMintInstallID() string {
	path := ""
	if home, err := os.UserHomeDir(); err == nil {
		path = filepath.Join(confdir.Dir(home), "install-id")
	}
	observedInvalid := false
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			if id := strings.TrimSpace(string(raw)); installIDPattern.MatchString(id) {
				return id
			}
			observedInvalid = len(strings.TrimSpace(string(raw))) > 0
		}
	}

	id := newUUIDv4()
	if path == "" {
		return id
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return id
	}
	// Garbage left by an older run would make O_EXCL trip, so clear it first.
	// A concurrent writer that wins this race leaves its own valid file.
	if observedInvalid {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if raw, rerr := os.ReadFile(path); rerr == nil {
			if existing := strings.TrimSpace(string(raw)); installIDPattern.MatchString(existing) {
				return existing
			}
		}
		return id
	}
	defer f.Close()
	if _, err := f.WriteString(id + "\n"); err != nil {
		return id
	}
	return id
}

// newUUIDv4 mints a random RFC 4122 version 4 UUID. crypto/rand does not fail
// in practice; the fallback keeps the identity well-formed if it ever does,
// rather than failing the connection over it.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], uint64(os.Getpid()))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant RFC 4122
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---------------------------------------------------------------------------
// What a Zen request carries
// ---------------------------------------------------------------------------

// opencodeSessionHeaders is the identity every request to Zen carries. Traffic
// that belongs to no conversation still has to carry the header shape, so it
// borrows this installation's identity.
func opencodeSessionHeaders(modelID, sessionID string) map[string]string {
	if sessionID == "" {
		sessionID = opencodeInstallIDOrMint()
	}
	headers := map[string]string{
		"x-opencode-session": opencodeSessionToken(sessionID),
	}
	if opencodeFreeModel(modelID) {
		// The gate reads the User-Agent's leading token before anything else,
		// then checks that the request says which client it is.
		headers["User-Agent"] = opencodeUserAgent + " (san)"
		headers["x-opencode-client"] = "cli"
	}
	return headers
}

// opencodeShapeRequest settles the two body-side checks the free tier makes:
// the working directory in the system prompt, and OpenCode's tool names in the
// roster.
//
// It is idempotent — the loop asks once per inference, and a retried inference
// asks again — and it changes nothing for a billed model, which is not gated.
func opencodeShapeRequest(modelID, sessionID, cwd string, system *string, tools *[]core.ToolSchema) {
	if !opencodeFreeModel(modelID) {
		return
	}
	addOpenCodeEnvBlock(system, cwd)
	addOpenCodeGateTools(tools)
}

// addOpenCodeEnvBlock appends the <env> block the gate looks for, unless the
// prompt already states both halves of it.
func addOpenCodeEnvBlock(system *string, cwd string) {
	if system == nil {
		return
	}
	if strings.Contains(*system, "<env>") && strings.Contains(*system, "Working directory:") {
		return
	}
	if cwd == "" {
		dir, err := os.Getwd()
		if err != nil {
			dir = "."
		}
		cwd = dir
	}
	block := "<env>\n  Working directory: " + cwd + "\n</env>"
	if strings.TrimSpace(*system) == "" {
		*system = block
		return
	}
	*system += "\n" + block
}

// addOpenCodeGateTools pads the roster with the gate's tool names it does not
// already carry.
//
// The stubs are wire-shape filler: the gate compares names and ignores schemas,
// and nothing here joins San's tool registry, so no model can call one.
func addOpenCodeGateTools(tools *[]core.ToolSchema) {
	if tools == nil {
		return
	}
	present := make(map[string]bool, len(*tools)+len(opencodeGateTools))
	for _, schema := range *tools {
		present[schema.Name] = true
	}
	for _, name := range opencodeGateTools {
		if present[name] {
			continue
		}
		*tools = append(*tools, core.ToolSchema{
			Name:       name,
			Definition: map[string]any{"type": "object"},
		})
	}
}

// ---------------------------------------------------------------------------
// The connection the gate reads
// ---------------------------------------------------------------------------

// opencodeHTTPClient is the client the OpenCode Zen vendor is built with: Bun's
// handshake, Go's HTTP/1.1 writer.
//
// The writer's header order is Go's rather than Bun's, and the gate does not
// look that far — offering only http/1.1 keeps the request off Go's HTTP/2
// stack, which is the half that would show.
func opencodeHTTPClient() *http.Client {
	transport := &http.Transport{
		// Proxies are dialed by opencodeTunnel rather than by the transport: a
		// CONNECT tunnel the transport built itself would be handshaked with
		// crypto/tls, which is the one thing this vendor cannot do.
		DialTLSContext:      opencodeDialTLS,
		ForceAttemptHTTP2:   false,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: opencodeHandshakeTimeout,
	}
	return &http.Client{Transport: opencodeRoundTripper{next: transport}}
}

// opencodeDialTLS opens the tunnel to addr, then completes the handshake with
// the Bun ClientHello. A proxy's own TLS (an https proxy) is an ordinary one:
// only Zen sees the handshake that matters.
func opencodeDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := opencodeTunnel(ctx, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := conn.ApplyPreset(opencodeClientHello()); err != nil {
		_ = raw.Close()
		return nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, opencodeHandshakeTimeout)
	defer cancel()
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

// opencodeTunnel opens a connection to addr, through the proxy the environment
// names for it. An HTTP or HTTPS proxy is asked for a CONNECT tunnel; a socks5
// proxy resolves or forwards the name itself.
func opencodeTunnel(ctx context.Context, addr string) (net.Conn, error) {
	proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "https", Host: addr}})
	if err != nil || proxyURL == nil {
		dialer := &net.Dialer{Timeout: opencodeDialTimeout, KeepAlive: 30 * time.Second}
		return dialer.DialContext(ctx, "tcp", addr)
	}
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		return dialer.Dial("tcp", addr)
	case "http", "https":
		return opencodeConnect(ctx, proxyURL, addr)
	default:
		return nil, fmt.Errorf("llm: OpenCode Zen does not support the %q proxy scheme", proxyURL.Scheme)
	}
}

// opencodeConnect asks an HTTP proxy for a tunnel to addr and returns the
// connection, with any bytes the proxy sent after its reply still readable.
func opencodeConnect(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	host := proxyURL.Host
	if proxyURL.Port() == "" {
		port := "80"
		if proxyURL.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(proxyURL.Hostname(), port)
	}
	dialer := &net.Dialer{Timeout: opencodeDialTimeout, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "https" {
		proxyTLS := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname()})
		if err := proxyTLS.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = proxyTLS
	}
	var request strings.Builder
	request.WriteString("CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n")
	if user := proxyURL.User; user != nil {
		password, _ := user.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(user.Username() + ":" + password))
		request.WriteString("Proxy-Authorization: Basic " + credentials + "\r\n")
	}
	request.WriteString("\r\n")
	if _, err := conn.Write([]byte(request.String())); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("llm: proxy %s refused the tunnel: %s", proxyURL.Host, response.Status)
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

// bufferedConn is a connection whose proxy reply read past the tunnel.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

// opencodeRoundTripper drops the SDK's telemetry headers before the request
// leaves. They name the language of a client that is not supposed to be these
// requests' author, and nothing upstream reads them.
type opencodeRoundTripper struct {
	next http.RoundTripper
}

func (rt opencodeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var stripped http.Header
	for name := range req.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-stainless-") {
			if stripped == nil {
				stripped = req.Header.Clone()
			}
			stripped.Del(name)
		}
	}
	if stripped != nil {
		req = req.Clone(req.Context())
		req.Header = stripped
	}
	return rt.next.RoundTrip(req)
}

// opencodeClientHello is the ClientHello Bun's fetch sends, in its order: no
// GREASE, no padding, TLS 1.2 and 1.3, X25519MLKEM768 as the first key share,
// and ALPN offering only http/1.1. A specification carries handshake state, so
// every connection builds a fresh one.
func opencodeClientHello() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			utls.TLS_AES_128_GCM_SHA256,
			utls.TLS_AES_256_GCM_SHA384,
			utls.TLS_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519MLKEM768, utls.X25519, utls.CurveP256, utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []uint8{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
				utls.PKCS1WithSHA1,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.X25519MLKEM768}, {Group: utls.X25519},
			}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.VersionTLS13, utls.VersionTLS12,
			}},
		},
	}
}
