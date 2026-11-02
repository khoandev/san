package llm

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// The transport Zen actually sees.
//
// Zen's free tier is closed to Go clients, and it is not the request that
// betrays them: the same key, headers and body sent from Bun answer 200 and
// from Go, curl, OpenSSL or Node answer 429 with a ~19h Retry-After. What the
// gate reads is the TLS ClientHello, which crypto/tls cannot change, so this
// vendor handshakes with the hello of the client OpenCode runs on.
//
// The components, measured from Bun's handshake: no GREASE, no padding, TLS
// 1.2 and 1.3, ALPN offering only http/1.1 (which also keeps the connection on
// HTTP/1.1, so the gate never sees a Go HTTP/2 stack), X25519MLKEM768 as the
// first key share, and the cipher list and extension order below. A
// specification carries handshake state, so every connection builds a fresh
// one.
//
// The HTTP/1.1 writer still belongs to net/http, so its header order is Go's
// rather than Bun's. The gate does not look that far.
const (
	opencodeDialTimeout      = 30 * time.Second
	opencodeHandshakeTimeout = 15 * time.Second
)

// opencodeHTTPClient is the client the OpenCode Zen vendor is built with.
func opencodeHTTPClient() *http.Client {
	transport := &http.Transport{
		// Proxies are dialed below rather than by the transport: a CONNECT
		// tunnel the transport built itself would then be handshaked with
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

// opencodeDialTLS dials addr through the environment's proxy, if any, and
// completes the handshake with the Bun ClientHello. A proxy's own TLS (an
// https proxy) is an ordinary one: only Zen sees the handshake that matters.
func opencodeDialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	raw, err := opencodeDialProxy(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		raw.Close()
		return nil, err
	}
	conn := utls.UClient(raw, &utls.Config{ServerName: host}, utls.HelloCustom)
	if err := conn.ApplyPreset(opencodeClientHello()); err != nil {
		raw.Close()
		return nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, opencodeHandshakeTimeout)
	defer cancel()
	if err := conn.HandshakeContext(handshakeCtx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// opencodeDialProxy opens a TCP connection to addr, through the proxy the
// environment names for it. An HTTP or HTTPS proxy is asked for a CONNECT
// tunnel; a socks5 proxy resolves or forwards the name itself.
func opencodeDialProxy(ctx context.Context, network, addr string) (net.Conn, error) {
	target := &url.URL{Scheme: "https", Host: addr}
	proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: target})
	if err != nil || proxyURL == nil {
		return opencodeDial(ctx, network, addr)
	}
	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		dialer, err := proxy.FromURL(proxyURL, proxy.Direct)
		if err != nil {
			return nil, err
		}
		return dialer.Dial(network, addr)
	case "http", "https":
		return opencodeConnect(ctx, proxyURL, addr)
	default:
		return nil, fmt.Errorf("llm: OpenCode Zen does not support the %q proxy scheme", proxyURL.Scheme)
	}
}

func opencodeDial(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: opencodeDialTimeout, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, addr)
}

// opencodeConnect asks an HTTP proxy for a tunnel to addr and returns the
// connection, with any bytes the proxy sent after its reply still readable.
func opencodeConnect(ctx context.Context, proxyURL *url.URL, addr string) (net.Conn, error) {
	host := proxyURL.Host
	if proxyURL.Port() == "" {
		if proxyURL.Scheme == "https" {
			host = net.JoinHostPort(proxyURL.Hostname(), "443")
		} else {
			host = net.JoinHostPort(proxyURL.Hostname(), "80")
		}
	}
	conn, err := opencodeDial(ctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	if proxyURL.Scheme == "https" {
		proxyTLS := tls.Client(conn, &tls.Config{ServerName: proxyURL.Hostname()})
		if err := proxyTLS.HandshakeContext(ctx); err != nil {
			conn.Close()
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
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		conn.Close()
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

// opencodeClientHello is the ClientHello Bun's fetch sends, in its order.
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
