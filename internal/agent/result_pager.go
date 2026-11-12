package agent

import (
	"context"

	"github.com/genai-io/san/internal/session"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// ResultPager keeps oversized text out of the model's conversation. Its store
// and session identity are captured at construction, never read from a mutable
// current-session selector. A missing/failed store still produces a bounded
// preview, explicitly reporting that the full content was not persisted.
//
// The bounding lives in session.PageToolResult because a subagent worker pages
// through the same function: a worker's window is paid for the same way its
// parent's is.
type ResultPager struct {
	Store     *session.LegacyStore
	SessionID string
}

func (p ResultPager) Filter(_ context.Context, c sdkagent.PostToolContext) (*sdkagent.Result, error) {
	page := p.Store.PageToolResult(p.SessionID, c.Call.ID, c.Result.Content.Text())
	if page == "" {
		return nil, nil
	}
	out := c.Result
	out.Content = ai.TextContent(page)
	return &out, nil
}
