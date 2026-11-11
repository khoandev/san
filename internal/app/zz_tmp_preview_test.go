package app

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/todo"
)

func TestZZTmpPreview(t *testing.T) {
	kit.InitSymbols(kit.IconStyleNerd)
	m := fixedComposerModel(100, 24)
	m.welcomePending = true
	m.env.CWD = "/Users/me/Project/sleeve"
	store := m.services.Tracker
	steps := []struct{ title, status string }{
		{"Đọc gateway spec", string(plan.StepCompleted)},
		{"Viết adapter cho Sleeve", string(plan.StepCompleted)},
		{"Kiểm chứng end-to-end với gateway giả", string(plan.StepInProgress)},
		{"Viết test hồi quy", string(plan.StepPending)},
		{"Cập nhật tài liệu", string(plan.StepPending)},
		{"Dọn dẹp config cũ", string(plan.StepPending)},
		{"Chạy toàn bộ suite", string(plan.StepPending)},
	}
	for i, s := range steps {
		st := todo.StatusPending
		if s.status == string(plan.StepCompleted) { st = todo.StatusCompleted }
		if s.status == string(plan.StepInProgress) { st = todo.StatusInProgress }
		it := store.Create(s.title, "", "", todo.PlanMetadata("p1", fmt.Sprintf("step-%d", i+1), s.status, ""))
		_ = store.Update(it.ID, todo.WithStatus(st))
	}
	frame, _ := m.viewString()
	fmt.Println(ansi.Strip(frame))
}
