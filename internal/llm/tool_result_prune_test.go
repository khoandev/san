package llm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/genai-io/san/internal/core"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// resultMessage answers call i with text of the given size.
func resultMessage(i, size int) core.Message {
	body := fmt.Sprintf("result-%02d\n%s", i, strings.Repeat("x", size))
	return ai.ToolResultsMessage(ai.ToolResult{ToolCallID: fmt.Sprintf("call-%02d", i), ToolName: "Read", Content: ai.TextContent(body)})
}

func conversation(results int, size int) []core.Message {
	messages := []core.Message{ai.UserMessage("start")}
	for i := range results {
		messages = append(messages,
			ai.Message{Role: ai.RoleAssistant, Content: ai.Content{ai.ToolCallBlock(ai.ToolCall{ID: fmt.Sprintf("call-%02d", i), Name: "Read", Input: `{}`})}},
			resultMessage(i, size))
	}
	return messages
}

// fingerprints renders every block a request or a conversation carries, so a
// transformation done in place shows up as a changed history.
func fingerprints(messages []core.Message) []string {
	var out []string
	for _, m := range messages {
		for _, block := range m.Content {
			switch {
			case block.ToolResult != nil:
				out = append(out, block.ToolResult.ToolCallID+"="+block.ToolResult.Text())
			case block.ToolCall != nil:
				out = append(out, "call:"+block.ToolCall.ID)
			default:
				out = append(out, block.Text)
			}
		}
	}
	return out
}

func TestPruneStaleToolResultsShrinksTheRequestNotTheConversation(t *testing.T) {
	const count = RecentToolResults + 4
	// Small enough that the window is bounded by the count, not by bytes.
	messages := conversation(count, 4000)
	before := fingerprints(messages)
	sent := PruneStaleToolResults(messages)
	if got := fingerprints(messages); !equalStrings(got, before) {
		t.Fatal("pruning changed the conversation it was asked to adapt")
	}

	if len(sent) != len(messages) {
		t.Fatalf("pruning dropped messages: %d of %d", len(sent), len(messages))
	}
	results := 0
	for i, m := range sent {
		for j, block := range m.Content {
			if block.ToolResult == nil {
				continue
			}
			results++
			original := messages[i].Content[j].ToolResult
			if block.ToolResult.ToolCallID != original.ToolCallID {
				t.Fatalf("result %d lost its call pairing", results)
			}
			full := results > count-RecentToolResults
			if full && block.ToolResult.Text() != original.Text() {
				t.Fatalf("recent result %d was not sent in full", results)
			}
			if !full && block.ToolResult.Text() == original.Text() {
				t.Fatalf("stale result %d was sent in full", results)
			}
			if !full && !strings.Contains(block.ToolResult.Text(), "elided") {
				t.Fatalf("stale result %d has no elision note: %q", results, block.ToolResult.Text())
			}
			if !full && !strings.HasPrefix(block.ToolResult.Text(), fmt.Sprintf("result-%02d", results-1)) {
				t.Fatalf("stale result %d lost its head: %q", results, block.ToolResult.Text())
			}
		}
	}
	if want := (len(messages) - 1) / 2; results != want {
		t.Fatalf("pruning changed the block count: %d results, want %d", results, want)
	}
	// Nothing in the request may write through to the conversation it copied,
	// so a mutation on the sent side leaves the stored result alone.
	sent[2].Content[0].ToolResult.Content[0].Text = "rewritten"
	if messages[2].Content[0].ToolResult.Text() == "rewritten" {
		t.Fatal("the request aliases a stored result's content")
	}
}

func TestPruneStaleToolResultsIsIdempotent(t *testing.T) {
	messages := conversation(RecentToolResults+3, 8000)
	once := PruneStaleToolResults(messages)
	twice := PruneStaleToolResults(once)
	if !equalStrings(fingerprints(once), fingerprints(twice)) {
		t.Fatal("pruning an already pruned request changed it")
	}
	// A later turn only moves the boundary forward: one more stale result
	// shrinks, and nothing already pruned is rewritten, so the request prefix a
	// prompt cache keys on does not churn.
	grown := append(append([]core.Message{}, messages...), resultMessage(99, 8000))
	was, now := resultTexts(once), resultTexts(PruneStaleToolResults(grown))
	for i := range was {
		pruned := strings.Contains(was[i], "elided")
		if pruned && was[i] != now[i] {
			t.Fatalf("result %d was rewritten after it had been pruned", i)
		}
		if !pruned && len(now[i]) > len(was[i]) {
			t.Fatalf("result %d grew", i)
		}
	}
}

// resultTexts is what each tool result says in one request, in order.
func resultTexts(messages []core.Message) []string {
	var out []string
	for _, m := range messages {
		for _, result := range m.Content.ToolResults() {
			out = append(out, result.Text())
		}
	}
	return out
}

func TestPruneStaleToolResultsBudgetsTheWindowInBytes(t *testing.T) {
	// Six 30 KB results: the newest two fit the budget, the rest are stale
	// whatever the count allows.
	sent := PruneStaleToolResults(conversation(6, 30_000))
	for i, text := range resultTexts(sent) {
		pruned := strings.Contains(text, "elided")
		if want := i < 4; pruned != want {
			t.Fatalf("result %d pruned=%v, want %v", i, pruned, want)
		}
	}
	// A single result over the whole budget still travels: it is the one the
	// next step reads.
	one := PruneStaleToolResults(conversation(1, 200_000))
	if strings.Contains(resultTexts(one)[0], "elided") {
		t.Fatal("the only result was pruned")
	}
}

func TestPruneStaleToolResultsKeepsShortResults(t *testing.T) {
	messages := conversation(RecentToolResults+3, 200)
	sent := PruneStaleToolResults(messages)
	if !equalStrings(fingerprints(sent), fingerprints(messages)) {
		t.Fatal("a small conversation was rewritten")
	}
}

func TestPruneStaleToolResultsDropsImagesButKeepsTheNote(t *testing.T) {
	messages := shootConversation(RecentToolResults + 1)
	sent := PruneStaleToolResults(messages)
	first := sent[1].Content[0].ToolResult
	if first.Content.HasImages() || !strings.Contains(first.Text(), "1 image") {
		t.Fatalf("stale image result kept its picture: %q", first.Text())
	}
	last := sent[len(sent)-1].Content[0].ToolResult
	if !last.Content.HasImages() {
		t.Fatal("a recent image result lost its picture")
	}
	if !messages[1].Content[0].ToolResult.Content.HasImages() {
		t.Fatal("pruning removed an image from the conversation")
	}
}

func TestPrepareRequestAdaptsCapabilitiesAndStaleness(t *testing.T) {
	messages := shootConversation(RecentToolResults + 1)
	newest := len(messages) - 1

	vision := NewClient(plainProvider{}, "m", 100).PrepareRequest(messages)
	if vision[1].Content.HasImages() || !strings.Contains(vision[1].Content[0].ToolResult.Text(), "elided") {
		t.Fatal("the oldest result reached a vision model in full")
	}
	if !vision[newest].Content[0].ToolResult.Content.HasImages() {
		t.Fatal("the newest result was pruned before its picture could be read")
	}

	textOnly := NewClient(textOnlyProvider{}, "m", 100).PrepareRequest(messages)
	if textOnly[newest].Content.HasImages() {
		t.Fatal("a text-only request still carries an image")
	}
	if !messages[1].Content[0].ToolResult.Content.HasImages() || !messages[newest].Content[0].ToolResult.Content.HasImages() {
		t.Fatal("the conversation lost an image")
	}
}

// shootConversation is n screenshot results: one line of text and a picture.
func shootConversation(n int) []core.Message {
	messages := []core.Message{ai.UserMessage("start")}
	for i := range n {
		messages = append(messages, ai.ToolResultsMessage(ai.ToolResult{
			ToolCallID: fmt.Sprintf("call-%02d", i),
			ToolName:   "Screenshot",
			Content:    ai.Content{ai.TextBlock("shot"), ai.ImageBlock(ai.Image{MediaType: "image/png", Data: "aW1n"})},
		}))
	}
	return messages
}

// BenchmarkPrepareRequestStaleToolResults reports the prompt this saves on a
// long turn: 40 results of 4 KB each, the shape of an hour of reading files.
func BenchmarkPrepareRequestStaleToolResults(b *testing.B) {
	messages := conversation(40, 4000)
	client := NewClient(plainProvider{}, "m", 100)
	for b.Loop() {
		if pruned := client.PrepareRequest(messages); len(pruned) == 0 {
			b.Fatal("no request")
		}
	}
	sent := (&ai.Request{Messages: client.PrepareRequest(messages)}).EstimateTokens()
	full := (&ai.Request{Messages: client.RequestMessages(messages)}).EstimateTokens()
	b.ReportMetric(float64(full), "tokens-before")
	b.ReportMetric(float64(sent), "tokens-after")
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
