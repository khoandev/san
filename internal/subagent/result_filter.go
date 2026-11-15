package subagent

import (
	"context"

	"github.com/genai-io/san/internal/core"
	sdkagent "github.com/genai-io/sdk-go/pkg/agent"
	"github.com/genai-io/sdk-go/pkg/ai"
)

// resultFilter bounds a worker's oversized tool results the way the main
// agent's ResultPager does, through the same session.PageToolResult. A worker
// keeps every result it reads in its own conversation for the rest of its
// turn, so one large file read is paid for again at each following step.
//
// A run without a live child session has no blob directory to point at, so it
// is left unfiltered: replacing a result there would hand the worker a
// truncated answer with nowhere to find the rest.
func (e *Executor) resultFilter(run *preparedRun) core.ResultFilter {
	store := run.store
	if store == nil || run.recording == nil {
		return nil
	}
	sessionID := run.recording.ID()
	return func(_ context.Context, c sdkagent.PostToolContext) (*sdkagent.Result, error) {
		page := store.PageToolResult(sessionID, c.Call.ID, c.Result.Content.Text())
		if page == "" {
			return nil, nil
		}
		out := c.Result
		out.Content = ai.TextContent(page)
		return &out, nil
	}
}
