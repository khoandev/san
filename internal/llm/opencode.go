package llm

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/genai-io/san/internal/confdir"
	"github.com/genai-io/san/internal/core"
)

// OpenCode Zen's wire identity.
//
// Zen's free tier is gated on client identity rather than on a credential, and
// the gate runs before the key is even looked at. Five things have to hold: the
// User-Agent leads with opencode/<version>, x-opencode-client names the client,
// x-opencode-session has the canonical ses_<12 hex><14 alnum> shape, the tool
// roster carries OpenCode's own five tool names, and the system prompt states
// the working directory in an <env> block. A request missing any of them is
// refused — which is why porting the exact wire behavior, rather than
// something equivalent, is the whole job.
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

// opencodeInstallID is this installation's stable identity, read once per
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
