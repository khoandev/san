package app

import (
	"errors"

	"github.com/genai-io/san/internal/hook"
	"github.com/genai-io/san/internal/llm"
	"github.com/genai-io/san/internal/session"
)

// bindSessionHooks captures the native owner; child engines must not reuse a
// parent's completion recorder or model-completion closure.
func bindSessionHooks(engine *hook.Engine, provider llm.Provider, owner *session.Session, model string) {
	bindSessionHooksOn(engine, provider, owner, model, "")
}

// bindSessionHooksOn is bindSessionHooks with a thinking effort for the hook
// model. The main conversation routes hooks to a cheap role, and that role's
// rung belongs to the binding, not to the session's own selection.
func bindSessionHooksOn(engine *hook.Engine, provider llm.Provider, owner *session.Session, model, effort string) {
	if engine == nil {
		return
	}
	engine.SetAuditCallback(nil)
	engine.SetLLMCompleter(nil, model)
	engine.SetRecordedLLMCompleter(buildRecordedHookCompleter(provider, owner, model, effort), model)
	engine.SetCompletionRecorder(session.NewRecorder(owner, "").RecordHook)
}

// closeSessionRecording runs after runtime and task shutdown, while hooks can
// still commit their outcomes to the original writer.
func (m *model) closeSessionRecording() error {
	if m.services.Hook != nil {
		m.services.Hook.Shutdown()
	}
	var err error
	if m.services.Session != nil {
		err = m.services.Session.Close()
	}
	if m.services.SessionStore != nil {
		err = errors.Join(err, m.services.SessionStore.Close())
	}
	return err
}
