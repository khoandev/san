package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/genai-io/san/internal/secret"
)

func TestPKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatalf("generatePKCE() error = %v", err)
	}
	if len(verifier) < 40 {
		t.Errorf("verifier too short: %q", verifier)
	}
	if challenge == "" || challenge == verifier {
		t.Errorf("invalid challenge: %q", challenge)
	}
}

func TestAntigravityState(t *testing.T) {
	encoded := encodeAntigravityState("test-verifier-123", "test-project-456")
	decoded, err := decodeAntigravityState(encoded)
	if err != nil {
		t.Fatalf("decodeAntigravityState() error = %v", err)
	}
	if decoded.Verifier != "test-verifier-123" {
		t.Errorf("decoded.Verifier = %q, want test-verifier-123", decoded.Verifier)
	}
	if decoded.ProjectID != "test-project-456" {
		t.Errorf("decoded.ProjectID = %q, want test-project-456", decoded.ProjectID)
	}
}

func TestTransformSSELine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain non-data line",
			input: "event: message\n",
			want:  "event: message\n",
		},
		{
			name:  "empty line",
			input: "\n",
			want:  "\n",
		},
		{
			name:  "done line",
			input: "data: [DONE]\n",
			want:  "data: [DONE]\n",
		},
		{
			name:  "wrapped response line",
			input: "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}}\n",
			want:  "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n",
		},
		{
			name:  "unwrapped response line passes through",
			input: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n",
			want:  "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(transformSSELine([]byte(tt.input)))
			if got != tt.want {
				t.Errorf("transformSSELine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestAntigravitySSEReader(t *testing.T) {
	input := "event: message\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"part1\"}]}}]}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"part2\"}]}}]}}\n\n" +
		"data: [DONE]\n\n"

	reader := newAntigravitySSEReader(io.NopCloser(strings.NewReader(input)))
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}

	got := string(out)
	if !strings.Contains(got, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"part1\"}]}}]}") {
		t.Errorf("missing unwrapped part1, got: %s", got)
	}
	if !strings.Contains(got, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"part2\"}]}}]}") {
		t.Errorf("missing unwrapped part2, got: %s", got)
	}
	if !strings.Contains(got, "data: [DONE]") {
		t.Errorf("missing [DONE], got: %s", got)
	}
}

func TestExtractModelIDFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/v1beta/models/gemini-3-flash:streamGenerateContent", "gemini-3-flash"},
		{"/models/claude-sonnet-4-6:generateContent", "claude-sonnet-4-6"},
		{"/v1beta/models/gemini-2.5-pro", "gemini-2.5-pro"},
		{"/other/path", ""},
	}

	for _, tt := range tests {
		got := extractModelIDFromPath(tt.path)
		if got != tt.want {
			t.Errorf("extractModelIDFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestAntigravityAuthenticatorRegistration(t *testing.T) {
	if !SupportsInteractiveLogin(Google, AuthAntigravity) {
		t.Fatal("Google:antigravity does not report interactive login support")
	}

	// Test credentials save and load
	fakeBlob := antigravityBlob{
		AccessToken:  "fake-access",
		RefreshToken: "fake-refresh",
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		ProjectID:    "fake-project",
		Email:        "user@example.com",
	}

	store := secret.Default()
	if store == nil {
		t.Skip("secret store unavailable")
	}

	if err := saveAntigravityBlob(fakeBlob); err != nil {
		t.Fatalf("saveAntigravityBlob() error = %v", err)
	}
	defer func() { _ = logoutAntigravity() }()

	if !hasAntigravityCredentials() {
		t.Error("hasAntigravityCredentials() = false after save")
	}

	loaded, ok, err := loadAntigravityBlob()
	if err != nil || !ok {
		t.Fatalf("loadAntigravityBlob() ok = %v, err = %v", ok, err)
	}
	if loaded.AccessToken != fakeBlob.AccessToken || loaded.Email != fakeBlob.Email {
		t.Errorf("loaded blob mismatch: %+v", loaded)
	}

	if err := logoutAntigravity(); err != nil {
		t.Fatalf("logoutAntigravity() error = %v", err)
	}
	if hasAntigravityCredentials() {
		t.Error("hasAntigravityCredentials() = true after logout")
	}
}

func TestAntigravityRoundTripperWrapsRequest(t *testing.T) {
	var receivedBody map[string]any
	var receivedHeaders http.Header
	var receivedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"chunk from server\"}]}}]}}\n\n")
	}))
	defer server.Close()

	// Setup fake credentials
	fakeBlob := antigravityBlob{
		AccessToken:  "valid-token",
		RefreshToken: "valid-refresh",
		ExpiresAt:    time.Now().Add(10 * time.Minute),
		ProjectID:    "test-project-xyz",
	}
	if err := saveAntigravityBlob(fakeBlob); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logoutAntigravity() }()

	// Custom transport pointing to test server
	serverURL, _ := url.Parse(server.URL)
	rt := antigravityRoundTripper{
		next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = serverURL.Scheme
			r.URL.Host = serverURL.Host
			return http.DefaultTransport.RoundTrip(r)
		}),
	}

	body := `{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}`
	req, _ := http.NewRequest("POST", "https://daily-cloudcode-pa.googleapis.com/v1beta/models/gemini-3-flash:streamGenerateContent?alt=sse", strings.NewReader(body))
	req.Header.Set("x-goog-api-key", "should-be-stripped")

	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip error = %v", err)
	}
	defer res.Body.Close()

	// Verify path was rewritten
	if receivedPath != "/v1internal:streamGenerateContent" {
		t.Errorf("receivedPath = %q, want /v1internal:streamGenerateContent", receivedPath)
	}

	// Verify headers
	if auth := receivedHeaders.Get("Authorization"); auth != "Bearer valid-token" {
		t.Errorf("Authorization header = %q, want Bearer valid-token", auth)
	}
	if userAgent := receivedHeaders.Get("User-Agent"); !strings.Contains(userAgent, "antigravity/hub/") {
		t.Errorf("User-Agent = %q, want antigravity/hub/...", userAgent)
	}
	if apiKey := receivedHeaders.Get("x-goog-api-key"); apiKey != "" {
		t.Errorf("x-goog-api-key was not stripped: %q", apiKey)
	}

	// Verify wrapped body
	if receivedBody["project"] != "test-project-xyz" {
		t.Errorf("wrapped project = %v, want test-project-xyz", receivedBody["project"])
	}
	if receivedBody["model"] != "gemini-3-flash" {
		t.Errorf("wrapped model = %v, want gemini-3-flash", receivedBody["model"])
	}
	if receivedBody["requestType"] != "agent" {
		t.Errorf("wrapped requestType = %v, want agent", receivedBody["requestType"])
	}
	if _, ok := receivedBody["request"].(map[string]any); !ok {
		t.Errorf("wrapped request missing or not map: %v", receivedBody["request"])
	}

	// Verify response was unwrapped
	respBytes, _ := io.ReadAll(res.Body)
	respStr := string(respBytes)
	if !strings.Contains(respStr, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"chunk from server\"}]}}]}") {
		t.Errorf("response was not unwrapped, got: %s", respStr)
	}
}

func TestAntigravityModelsList(t *testing.T) {
	// Without live server, fallback models should be returned
	res, err := handleAntigravityModelsList(context.Background(), "invalid-token", "invalid-proj")
	if err != nil {
		t.Fatalf("handleAntigravityModelsList() error = %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}

	var data struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	if len(data.Models) == 0 {
		t.Fatal("no models returned")
	}

	foundFlash := false
	for _, m := range data.Models {
		if m.Name == "models/gemini-3-flash" {
			foundFlash = true
			break
		}
	}
	if !foundFlash {
		t.Errorf("gemini-3-flash not found in fallback models: %+v", data.Models)
	}
}

func TestAntigravitySystemInstructionInjection(t *testing.T) {
	// 1. Without existing systemInstruction: should inject default antigravity persona
	req := map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": []any{map[string]any{"text": "hello"}},
			},
		},
	}
	wrapped := prepareAntigravityPayload("gemini-3-flash", "test-proj", req)
	innerReq := wrapped["request"].(map[string]any)

	sysInst, ok := innerReq["systemInstruction"].(map[string]any)
	if !ok {
		t.Fatalf("systemInstruction missing or not map: %v", innerReq["systemInstruction"])
	}
	if sysInst["role"] != "user" {
		t.Errorf("systemInstruction role = %v, want user", sysInst["role"])
	}
	parts := sysInst["parts"].([]any)
	firstPart := parts[0].(map[string]any)
	text := firstPart["text"].(string)
	if !strings.Contains(text, "You are Antigravity, a powerful agentic AI coding assistant") {
		t.Errorf("systemInstruction does not contain Antigravity persona: %s", text)
	}

	// 2. With existing user system instruction: should prepend Antigravity persona
	reqWithSys := map[string]any{
		"systemInstruction": "Keep responses short.",
		"contents":          []any{},
	}
	wrapped2 := prepareAntigravityPayload("gemini-3-flash", "test-proj", reqWithSys)
	innerReq2 := wrapped2["request"].(map[string]any)
	sysInst2 := innerReq2["systemInstruction"].(map[string]any)
	text2 := sysInst2["parts"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text2, "You are Antigravity") || !strings.Contains(text2, "Keep responses short.") {
		t.Errorf("systemInstruction failed to combine: %s", text2)
	}

	// 3. For Claude thinking model: should also append interleaved thinking hint
	wrappedClaude := prepareAntigravityPayload("claude-opus-4-6-thinking", "test-proj", req)
	innerClaude := wrappedClaude["request"].(map[string]any)
	sysClaude := innerClaude["systemInstruction"].(map[string]any)
	textClaude := sysClaude["parts"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(textClaude, "Interleaved thinking is enabled") {
		t.Errorf("Claude thinking model missing interleaved thinking hint: %s", textClaude)
	}
}

func TestAntigravityThoughtSignatureSentinel(t *testing.T) {
	// Two functionCall parts in one block (parallel tool calling)
	contents := []any{
		map[string]any{
			"role": "model",
			"parts": []any{
				map[string]any{
					"functionCall": map[string]any{
						"name": "read_file",
						"args": map[string]any{"path": "foo.txt"},
					},
					// Missing thoughtSignature
				},
				map[string]any{
					"functionCall": map[string]any{
						"name": "read_file",
						"args": map[string]any{"path": "bar.txt"},
					},
					"thoughtSignature": "some-invalid-short-sig",
				},
			},
		},
	}

	req := map[string]any{"contents": contents}
	wrapped := prepareAntigravityPayload("gemini-3-flash", "test-proj", req)
	inner := wrapped["request"].(map[string]any)
	newContents := inner["contents"].([]any)
	c0 := newContents[0].(map[string]any)
	parts := c0["parts"].([]any)

	// First call MUST have skip_thought_signature_validator
	p0 := parts[0].(map[string]any)
	if p0["thoughtSignature"] != "skip_thought_signature_validator" {
		t.Errorf("part 0 thoughtSignature = %v, want skip_thought_signature_validator", p0["thoughtSignature"])
	}
	if p0["thought_signature"] != "skip_thought_signature_validator" {
		t.Errorf("part 0 thought_signature = %v, want skip_thought_signature_validator", p0["thought_signature"])
	}

	// Second call MUST NOT have thoughtSignature (parallel calls cannot have signature)
	p1 := parts[1].(map[string]any)
	if _, exists := p1["thoughtSignature"]; exists {
		t.Errorf("part 1 has unexpected thoughtSignature: %v", p1["thoughtSignature"])
	}
	if _, exists := p1["thought_signature"]; exists {
		t.Errorf("part 1 has unexpected thought_signature: %v", p1["thought_signature"])
	}
}

func TestAntigravityClaudeToolPairingAndValidation(t *testing.T) {
	contents := []any{
		map[string]any{
			"role": "model",
			"parts": []any{
				map[string]any{
					"functionCall": map[string]any{
						"name": "read_file",
						"args": map[string]any{"path": "main.go"},
					},
				},
			},
		},
		map[string]any{
			"role": "user",
			"parts": []any{
				map[string]any{
					"functionResponse": map[string]any{
						"name":     "read_file",
						"response": map[string]any{"content": "package main"},
					},
				},
			},
		},
	}

	req := map[string]any{
		"contents": contents,
		"tools": []any{
			map[string]any{
				"name":        "read_file",
				"description": "Read file content",
				// empty schema
			},
		},
	}

	wrapped := prepareAntigravityPayload("claude-sonnet-4-6", "test-proj", req)
	inner := wrapped["request"].(map[string]any)

	// Check toolConfig mode VALIDATED
	toolConfig := inner["toolConfig"].(map[string]any)
	fcConfig := toolConfig["functionCallingConfig"].(map[string]any)
	if fcConfig["mode"] != "VALIDATED" {
		t.Errorf("toolConfig mode = %v, want VALIDATED", fcConfig["mode"])
	}

	// Check ID pairing between functionCall and functionResponse
	newContents := inner["contents"].([]any)
	callPart := newContents[0].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	respPart := newContents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)

	callID := callPart["id"].(string)
	respID := respPart["id"].(string)
	if callID == "" || respID == "" || callID != respID {
		t.Errorf("mismatched tool IDs: callID=%q, respID=%q", callID, respID)
	}

	// Check placeholder schema injected for empty Claude schema
	tools := inner["tools"].([]any)
	fdGroup := tools[0].(map[string]any)["functionDeclarations"].([]any)
	fd := fdGroup[0].(map[string]any)
	params := fd["parameters"].(map[string]any)
	props := params["properties"].(map[string]any)
	if _, hasPlaceholder := props["_placeholder"]; !hasPlaceholder {
		t.Errorf("expected _placeholder in Claude empty schema parameters: %+v", params)
	}
}

func TestAntigravityJSONSchemaSanitization(t *testing.T) {
	rawSchema := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"filePath": map[string]any{
				"type": "string",
			},
			"tags": map[string]any{
				"type": "array",
				// missing items
			},
		},
		"required": []any{"filePath", "nonExistentField"},
	}

	cleanedGemini := cleanJSONSchemaForAntigravity(rawSchema, false)
	if _, exists := cleanedGemini["$schema"]; exists {
		t.Error("$schema was not stripped")
	}
	if _, exists := cleanedGemini["additionalProperties"]; exists {
		t.Error("additionalProperties was not stripped")
	}
	if cleanedGemini["type"] != "OBJECT" {
		t.Errorf("type = %v, want OBJECT", cleanedGemini["type"])
	}
	props := cleanedGemini["properties"].(map[string]any)
	tagsProp := props["tags"].(map[string]any)
	if tagsProp["items"] == nil {
		t.Error("array items was not filled")
	}
	reqList := cleanedGemini["required"].([]any)
	if len(reqList) != 1 || reqList[0] != "filePath" {
		t.Errorf("required was not cleaned properly: %+v", reqList)
	}
}

func TestAntigravitySyntheticProjectID(t *testing.T) {
	id1 := generateSyntheticProjectID()
	id2 := generateSyntheticProjectID()
	if id1 == "" || id2 == "" || id1 == id2 {
		t.Errorf("synthetic IDs should be non-empty and unique: %q, %q", id1, id2)
	}
	parts := strings.Split(id1, "-")
	if len(parts) != 3 {
		t.Errorf("synthetic ID should have 3 parts (adj-noun-hex): %q", id1)
	}
}

func TestAntigravityModelIDResolution(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"antigravity-gemini-3-flash", "gemini-3-flash"},
		{"models/antigravity-claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"models/gemini-3-pro", "gemini-3-pro"},
	}
	for _, tt := range tests {
		got := resolveAntigravityModelID(tt.input)
		if got != tt.want {
			t.Errorf("resolveAntigravityModelID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
