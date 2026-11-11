package conv

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/genai-io/san/internal/app/kit"
	"github.com/genai-io/san/internal/plan"
	"github.com/genai-io/san/internal/todo"
)

// taskIDRe matches "#<number>" task tags in rendered output.
var taskIDRe = regexp.MustCompile(`#\d+`)

// taskRowRe captures the subject number of a rendered row, so a test can say
// which rows survived windowing without depending on the ordinal column.
var taskRowRe = regexp.MustCompile(`\bTask (\d+)\b`)

// numbersOf extracts the first capture group from successive regexp matches.
func numbersOf(matches [][]string) []string {
	numbers := make([]string, 0, len(matches))
	for _, match := range matches {
		numbers = append(numbers, match[1])
	}
	return numbers
}

func TestRenderTrackerListShowsTaskStatus(t *testing.T) {
	todo.Initialize()
	t.Cleanup(func() { todo.Default().Reset() })

	inProgress := todo.Default().Create("Fix auth module", "", "", map[string]any{
		"background_task_id":       "bg-1",
		"background_status_detail": "running",
	})
	_ = todo.Default().Update(inProgress.ID, todo.WithStatus(todo.StatusInProgress))

	failed := todo.Default().Create("Fix payment module", "", "", map[string]any{
		"background_task_id":       "bg-2",
		"background_status_detail": "failed",
	})
	_ = todo.Default().Update(failed.ID, todo.WithStatus(todo.StatusCompleted))

	completed := todo.Default().Create("Ship feature", "", "", nil)
	_ = todo.Default().Update(completed.ID, todo.WithStatus(todo.StatusCompleted))

	pending := todo.Default().Create("Write tests", "", "", nil)
	_ = todo.Default().Update(pending.ID, todo.WithStatus(todo.StatusPending))

	view := RenderTrackerList(TrackerListParams{
		CompletionShown: func(string) bool { return false },
		Items:           todo.Default().List(),
		StreamActive:    true,
		Width:           120,
		Blockers:        todo.Default().OpenBlockers,
		Executing:       func(*todo.Item) bool { return true },
	})
	plain := stripANSI(view)

	for _, want := range []string{
		"Todos",
		"1/2 done",
		"●",
		"Fix auth module",
		"●",
		"Fix payment module",
		"[failed]",
		"✓",
		"Ship feature",
		"○",
		"Write tests",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered tracker list missing %q:\n%s", want, plain)
		}
	}
}

// The running row is told apart by the accent color and a solid dot, never by
// motion: a list a person is reading must not blink at them.
func TestRenderTaskHoldsStillWhileRunning(t *testing.T) {
	task := &todo.Item{ID: "1", Subject: "Fix auth module", Status: todo.StatusInProgress}
	num := itemNumber{tag: "1.", width: 2}

	first := renderItem(task, itemRunning, 80, num, nil, nil, nil)
	if !strings.Contains(stripANSI(first), kit.CurrentSymbols.LiveDot) {
		t.Fatalf("in-progress task lost the live dot: %q", stripANSI(first))
	}
	for range 8 {
		if again := renderItem(task, itemRunning, 80, num, nil, nil, nil); again != first {
			t.Fatalf("in-progress task changed between renders:\n%q\n%q", first, again)
		}
	}
}

func TestRenderTrackerListOrdersByID(t *testing.T) {
	todo.Initialize()
	t.Cleanup(func() { todo.Default().Reset() })

	// Create tasks with mixed statuses — an in-progress task after a pending one.
	pending1 := todo.Default().Create("Pending A", "", "", nil)
	_ = todo.Default().Update(pending1.ID, todo.WithStatus(todo.StatusPending))

	inProgress := todo.Default().Create("Active B", "", "", nil)
	_ = todo.Default().Update(inProgress.ID, todo.WithStatus(todo.StatusInProgress))

	pending2 := todo.Default().Create("Pending C", "", "", nil)
	_ = todo.Default().Update(pending2.ID, todo.WithStatus(todo.StatusPending))

	view := RenderTrackerList(TrackerListParams{
		CompletionShown: func(string) bool { return false },
		Items:           todo.Default().List(),
		StreamActive:    true,
		Width:           120,
		Executing:       func(*todo.Item) bool { return true },
	})
	plain := stripANSI(view)

	// The ordinal column is the reading order: one sequence, one row each.
	for _, want := range []string{"1. Pending A", "2. Active B", "3. Pending C"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("row %q missing or renumbered:\n%s", want, plain)
		}
	}
	if first, second, third := strings.Index(plain, "Pending A"), strings.Index(plain, "Active B"), strings.Index(plain, "Pending C"); first > second || second > third {
		t.Fatalf("rows are not in recorded order:\n%s", plain)
	}
}

// A task marked in progress whose executor is gone must render identically on
// every frame. The status field alone used to drive the pulse, so a task the
// model never closed out kept animating after the turn ended.
func TestRenderTaskHoldsStillWhenStalled(t *testing.T) {
	task := &todo.Item{ID: "1", Subject: "Fix auth module", Status: todo.StatusInProgress}

	num := itemNumber{tag: "1.", width: 2}
	first := stripANSI(renderItem(task, itemStalled, 80, num, nil, nil, nil))
	for range 8 {
		if frame := stripANSI(renderItem(task, itemStalled, 80, num, nil, nil, nil)); frame != first {
			t.Fatalf("stalled task animated across frames:\n%q\n%q", first, frame)
		}
	}
	if !strings.Contains(first, "[stalled]") {
		t.Fatalf("stalled task should be labelled as such, got %q", first)
	}
}

// Only successful completions can hide the panel; stalled or aborted work stays.
func TestRenderTrackerListVisibility(t *testing.T) {
	stalled := &todo.Item{ID: "1", Subject: "Fix auth module", Status: todo.StatusInProgress}
	done := &todo.Item{ID: "1", Subject: "Fix auth module", Status: todo.StatusCompleted}
	worker := func(detail string) *todo.Item {
		return &todo.Item{ID: "2", Subject: "Upload video", Status: todo.StatusCompleted, Metadata: map[string]any{
			"background_task_id":       "bg-1",
			"background_status_detail": detail,
		}}
	}

	cases := []struct {
		name         string
		tasks        []*todo.Item
		streamActive bool
		wantVisible  bool
	}{
		{"no tasks", nil, true, false},
		{"complete and idle", []*todo.Item{done}, false, false},
		{"complete but still streaming", []*todo.Item{done}, true, true},
		{"stalled item keeps the list up", []*todo.Item{stalled}, false, true},
		{"worker complete and idle", []*todo.Item{worker("completed")}, false, false},
		{"worker complete while streaming", []*todo.Item{worker("completed")}, true, false},
		{"failed worker stays when idle", []*todo.Item{worker("failed")}, false, true},
		{"killed worker stays when idle", []*todo.Item{worker("killed")}, false, true},
		{"stopped worker stays when idle", []*todo.Item{worker("stopped")}, false, true},
		{"interrupted worker stays when idle", []*todo.Item{worker(todo.StatusDetailInterrupted)}, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := RenderTrackerList(TrackerListParams{
				Items:        tc.tasks,
				StreamActive: tc.streamActive,
				Width:        120,
				Executing:    func(*todo.Item) bool { return false },
			})
			if visible := view != ""; visible != tc.wantVisible {
				t.Fatalf("visible = %v, want %v; rendered:\n%s", visible, tc.wantVisible, stripANSI(view))
			}
		})
	}
}

// Windowing must keep the rows that are moving: in-progress work is classified
// from its recorded status (itemStalled), and it takes the slots before history
// does.
func TestRenderTrackerListKeepsRunningRowsOnOverflow(t *testing.T) {
	items := make([]*todo.Item, 0, maxVisibleItems+3)
	for i := 1; i <= maxVisibleItems+3; i++ {
		id := strconv.Itoa(i)
		items = append(items, &todo.Item{ID: id, Subject: "Task " + id, Status: todo.StatusCompleted})
	}
	items[len(items)-2].Status = todo.StatusInProgress // #10
	items[len(items)-1].Status = todo.StatusInProgress // #11

	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        120,
		Executing:    func(*todo.Item) bool { return true },
	}))

	for _, want := range []string{"Task 10", "Task 11"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("running task %q folded away behind finished work:\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "3 completed") {
		t.Fatalf("folded finished items were not summarized:\n%s", plain)
	}
}

// A blocked plan step is work waiting on the person, so it keeps its row before
// pending and finished work; the overflow line names how many are hidden.
func TestRenderTrackerListSummarizesHiddenBlockedSteps(t *testing.T) {
	items := make([]*todo.Item, 0, 11)
	for i := 1; i <= 10; i++ {
		id := strconv.Itoa(i)
		items = append(items, &todo.Item{
			ID:       id,
			Subject:  "Blocked " + id,
			Status:   todo.StatusPending,
			Metadata: todo.PlanMetadata("plan-1", "step-"+id, string(plan.StepBlocked), "waiting on the API"),
		})
	}
	items = append(items, &todo.Item{ID: "11", Subject: "Running", Status: todo.StatusInProgress})

	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        120,
		Executing:    func(*todo.Item) bool { return true },
	}))

	if !strings.Contains(plain, "Running") {
		t.Fatalf("the running row was folded away:\n%s", plain)
	}
	if !strings.Contains(plain, "Blocked 10") {
		t.Fatalf("the newest blocked row was folded away:\n%s", plain)
	}
	if strings.Contains(plain, "Blocked 1\n") {
		t.Fatalf("the oldest blocked row should be the one hidden:\n%s", plain)
	}
	if !strings.Contains(plain, "3 blocked steps") {
		t.Fatalf("hidden blocked rows were not summarized:\n%s", plain)
	}
}

// phaseOf reads the plan's own step status: blocked is not a todo status, but
// it must not read as a fresh pending row. A completed row outranks stale
// metadata.
func TestPhaseOfReadsBlockedPlanStep(t *testing.T) {
	blocked := &todo.Item{
		Subject:  "Wire the panel",
		Status:   todo.StatusPending,
		Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepBlocked), "the API moved"),
	}
	if got := phaseOf(blocked, false); got != itemBlocked {
		t.Fatalf("blocked plan step phase = %d, want itemBlocked", got)
	}

	stale := &todo.Item{
		Subject:  "Wire the panel",
		Status:   todo.StatusCompleted,
		Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepBlocked), "the API moved"),
	}
	if got := phaseOf(stale, false); got != itemFinished {
		t.Fatalf("completed step phase = %d, want itemFinished", got)
	}
}

// Completed rows yield space to unfinished work when the list overflows.
func TestRenderTrackerListFoldsLeadingFinished(t *testing.T) {
	tasks := make([]*todo.Item, 0, maxVisibleItems+3)
	for i := 1; i <= maxVisibleItems+3; i++ {
		id := strconv.Itoa(i)
		tasks = append(tasks, &todo.Item{
			ID:      id,
			Subject: "Task " + id,
			Status:  todo.StatusCompleted,
		})
	}
	// The tail is the work still in flight — it must never fold away.
	tasks[len(tasks)-3].Status = todo.StatusInProgress // #9
	tasks[len(tasks)-2].Status = todo.StatusPending    // #10
	tasks[len(tasks)-1].Status = todo.StatusPending    // #11

	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        tasks,
		StreamActive: true,
		Width:        120,
		Executing:    func(*todo.Item) bool { return true },
	}))

	ids := numbersOf(taskRowRe.FindAllStringSubmatch(plain, -1))
	want := []string{"4", "5", "6", "7", "8", "9", "10", "11"}
	if !slices.Equal(ids, want) {
		t.Fatalf("visible tasks:\n  got:  %v\n  want: %v\n\nfull output:\n%s", ids, want, plain)
	}
	if !strings.Contains(plain, "3 completed") {
		t.Fatalf("folded finished items should be summarized, not dropped silently:\n%s", plain)
	}
}

func TestRenderTrackerListLimitsBackgroundTasks(t *testing.T) {
	cases := []struct {
		name          string
		count         int
		status        string
		overrides     map[int]string
		wantIDs       []string
		wantSummaries []string
	}{
		{
			name:    "at the limit",
			count:   8,
			status:  todo.StatusInProgress,
			wantIDs: []string{"1", "2", "3", "4", "5", "6", "7", "8"},
		},
		{
			name:   "interleaved completions",
			count:  11,
			status: todo.StatusCompleted,
			overrides: map[int]string{
				1: todo.StatusInProgress, 3: todo.StatusInProgress,
				5: todo.StatusPending, 9: todo.StatusInProgress,
			},
			wantIDs: []string{"1", "3", "5", "9"},
		},
		{
			name:          "many running agents",
			count:         25,
			status:        todo.StatusInProgress,
			wantIDs:       []string{"18", "19", "20", "21", "22", "23", "24", "25"},
			wantSummaries: []string{"17 more in progress"},
		},
		{
			name:          "in progress before pending",
			count:         11,
			status:        todo.StatusPending,
			overrides:     map[int]string{1: todo.StatusInProgress},
			wantIDs:       []string{"1", "5", "6", "7", "8", "9", "10", "11"},
			wantSummaries: []string{"3 more pending"},
		},
		{
			name:          "abnormal endings stay after completions leave",
			count:         11,
			status:        todo.StatusCompleted,
			overrides:     map[int]string{1: "failed", 2: "killed", 3: "stopped", 4: todo.StatusDetailInterrupted},
			wantSummaries: []string{"4 past tasks"},
		},
		{
			name:          "many failed agents",
			count:         11,
			status:        "failed",
			wantSummaries: []string{"11 past tasks"},
		},
		{
			name:          "hidden failures stay in the summary",
			count:         11,
			status:        todo.StatusInProgress,
			overrides:     map[int]string{11: "failed"},
			wantIDs:       []string{"3", "4", "5", "6", "7", "8", "9", "10"},
			wantSummaries: []string{"2 more in progress", "1 past task"},
		},
	}
	rowRE := regexp.MustCompile(`\bTask (\d+)\b`)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := make([]*todo.Item, 0, tc.count)
			for i := 1; i <= tc.count; i++ {
				id := strconv.Itoa(i)
				status := tc.status
				if override, ok := tc.overrides[i]; ok {
					status = override
				}
				metadata := map[string]any{
					"background_task_id":   "bg-" + id,
					"background_task_type": "agent",
				}
				switch status {
				case "failed", "killed", "stopped", todo.StatusDetailInterrupted:
					metadata["background_status_detail"] = status
					status = todo.StatusCompleted
				}
				items = append(items, &todo.Item{ID: id, Subject: "Task " + id, Status: status, Metadata: metadata})
			}
			checks := 0
			plain := stripANSI(RenderTrackerList(TrackerListParams{
				Items: items, StreamActive: true, Width: 120,
				Executing: func(*todo.Item) bool {
					checks++
					return true
				},
			}))
			ids := numbersOf(rowRE.FindAllStringSubmatch(plain, -1))
			if !slices.Equal(ids, tc.wantIDs) {
				t.Fatalf("visible tasks = %v, want %v:\n%s", ids, tc.wantIDs, plain)
			}
			for _, want := range tc.wantSummaries {
				if !strings.Contains(plain, want) {
					t.Fatalf("missing overflow summary %q:\n%s", want, plain)
				}
			}
			wantLines := len(tc.wantIDs)
			if len(tc.wantIDs) > 0 {
				wantLines++ // Only current rows need a Tasks heading.
			}
			if len(tc.wantSummaries) > 0 {
				wantLines++
			}
			if lines := strings.Count(plain, "\n"); lines != wantLines {
				t.Fatalf("panel has %d lines, want %d:\n%s", lines, wantLines, plain)
			}
			if checks != len(tc.wantIDs) {
				t.Fatalf("queried %d executors for %d visible rows", checks, len(tc.wantIDs))
			}
		})
	}
}

// A row owned by a background agent is tinted with that agent's color; a plain
// todo keeps the status palette. The tint changes only color, never the text.
func TestRenderItemTintsAgentOwnedRows(t *testing.T) {
	item := &todo.Item{ID: "1", Subject: "Audit deps", Status: todo.StatusInProgress, Owner: "explorer"}
	colors := map[string]string{"explorer": "yellow"}

	num := itemNumber{tag: "1.", width: 2}
	plain := renderItem(item, itemRunning, 80, num, nil, nil, nil)
	tinted := renderItem(item, itemRunning, 80, num, nil, colors, nil)

	if stripANSI(plain) != stripANSI(tinted) {
		t.Fatalf("agent tint changed the row text, not just its color:\n  %q\n  %q", stripANSI(plain), stripANSI(tinted))
	}
	if plain == tinted {
		t.Fatalf("agent-owned row should be tinted with the agent color, got identical output:\n%q", tinted)
	}
}

func TestAgentForeground(t *testing.T) {
	colors := map[string]string{"explorer": "yellow"}

	if _, ok := agentForeground(&todo.Item{Owner: "explorer"}, colors); !ok {
		t.Fatal("agent-owned item should resolve its color")
	}
	if _, ok := agentForeground(&todo.Item{Owner: "Explorer"}, colors); !ok {
		t.Fatal("owner lookup should be case-insensitive")
	}
	if _, ok := agentForeground(&todo.Item{Owner: ""}, colors); ok {
		t.Fatal("a plain todo (no owner) must not resolve a color")
	}
	if _, ok := agentForeground(&todo.Item{Owner: "ghost"}, colors); ok {
		t.Fatal("an owner with no configured color must not resolve one")
	}
}

func TestPhaseOf(t *testing.T) {
	worker := func(statusDetail string) map[string]any {
		return map[string]any{
			"background_task_id":       "bg-1",
			"background_status_detail": statusDetail,
		}
	}

	cases := []struct {
		name      string
		status    string
		metadata  map[string]any
		executing bool
		want      itemPhase
	}{
		{"pending", todo.StatusPending, nil, false, itemWaiting},
		{"in progress with executor", todo.StatusInProgress, nil, true, itemRunning},
		{"in progress without executor", todo.StatusInProgress, nil, false, itemStalled},
		{"completed", todo.StatusCompleted, nil, false, itemFinished},
		{"worker finished", todo.StatusCompleted, worker(""), false, itemFinished},
		{"worker failed", todo.StatusCompleted, worker("failed"), false, itemAborted},
		{"worker killed", todo.StatusCompleted, worker("killed"), false, itemAborted},
		{"worker stopped", todo.StatusCompleted, worker("stopped"), false, itemAborted},
		{"worker orphaned", todo.StatusCompleted, worker(todo.StatusDetailInterrupted), false, itemAborted},
		// A terminal status wins regardless of what the executor says, so a
		// stale "still running" answer cannot resurrect a finished task.
		{"completed despite executor", todo.StatusCompleted, nil, true, itemFinished},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := &todo.Item{ID: "1", Subject: "Task", Status: tc.status, Metadata: tc.metadata}
			if got := phaseOf(task, tc.executing); got != tc.want {
				t.Fatalf("phaseOf = %v, want %v", got, tc.want)
			}
		})
	}
}

// Background rows describe the work directly and stay out of plan progress.
func TestRenderTrackerListBackgroundRowsDescribeWork(t *testing.T) {
	todo.Initialize()
	t.Cleanup(func() { todo.Default().Reset() })

	worker := todo.Default().Create("Save Jenkins script", "", "", map[string]any{
		"background_task_id":   "bg-1",
		"background_task_type": "bash",
	})
	_ = todo.Default().Update(worker.ID, todo.WithStatus(todo.StatusInProgress))
	render := func() string {
		return stripANSI(RenderTrackerList(TrackerListParams{
			Items:     todo.Default().List(),
			Width:     120,
			Executing: func(*todo.Item) bool { return true },
		}))
	}

	plain := render()
	if !strings.HasPrefix(plain, "  Todos\n") || strings.Contains(plain, "%") || strings.Count(plain, "\n") != 2 {
		t.Errorf("a worker should render under the Todos heading without progress counts:\n%s", plain)
	}
	if !strings.Contains(plain, "● Save Jenkins script") || strings.Contains(plain, "bash") || taskIDRe.MatchString(plain) {
		t.Errorf("worker row should lead with the work description:\n%s", plain)
	}
	if strings.Contains(plain, "● 1.") {
		t.Errorf("a background worker must not take an ordinal:\n%s", plain)
	}

	// A session todo counts toward the heading; a background worker does not.
	own := todo.Default().Create("Write tests", "", "", nil)
	_ = todo.Default().Update(own.ID, todo.WithStatus(todo.StatusCompleted))
	if plain := render(); !strings.Contains(plain, "1/1 done") || strings.Count(plain, "Todos") != 1 {
		t.Errorf("progress should count session todos, not background workers:\n%s", plain)
	}
}

// The heading names the plan, counts its finished steps, and reports why the
// executor is not moving — all on the one row the strip can spend on identity.
func TestTrackerStripHeaderNamesPlanAndCounts(t *testing.T) {
	planRow := func(id, subject, status string, kind plan.StepStatus) *todo.Item {
		return &todo.Item{
			ID:       id,
			Subject:  subject,
			Status:   status,
			Metadata: todo.PlanMetadata("plan-1", "step-"+id, string(kind), ""),
		}
	}
	items := []*todo.Item{
		planRow("1", "read code", todo.StatusCompleted, plan.StepCompleted),
		planRow("2", "move files", todo.StatusInProgress, plan.StepInProgress),
		planRow("3", "add tests", todo.StatusPending, plan.StepPending),
		planRow("4", "ship it", todo.StatusPending, plan.StepPending),
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:          items,
		StreamActive:   true,
		Width:          100,
		PlanTitle:      "Split auth",
		PlanStatusNote: "1 blocked step",
		Paused:         true,
		Awaiting:       true,
		Executing:      func(*todo.Item) bool { return true },
	}))

	for _, want := range []string{
		"Plan · Split auth",
		"1/4 done",
		"▰▱▱▱▱",
		"1 blocked step",
		"paused",
		"waiting for you",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("heading lost %q:\n%s", want, plain)
		}
	}
	// The rows follow the plan's own states, one row each.
	for _, want := range []string{"✓ 1. read code", "● 2. move files", "○ 3. add tests"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("row missing %q:\n%s", want, plain)
		}
	}
}

// Finished plan steps stay on screen struck through, even once the turn is
// idle: the list is the record of what just ran.
func TestTrackerStripStrikesFinishedRows(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read code", Status: todo.StatusCompleted,
			Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepCompleted), "")},
		{ID: "2", Subject: "old idea", Status: todo.StatusCompleted,
			Metadata: todo.PlanMetadata("plan-1", "step-2", string(plan.StepSkipped), "")},
	}
	view := RenderTrackerList(TrackerListParams{Items: items, Width: 80, PlanTitle: "Split auth"})
	if view == "" {
		t.Fatal("a completed plan's rows disappeared while idle")
	}
	if on, off := strings.Count(view, "\x1b[9m"), strings.Count(view, "\x1b[29m"); on != 2 || off != 2 {
		t.Fatalf("finished rows are not struck through (%d on, %d off):\n%q", on, off, view)
	}
	plain := stripANSI(view)
	if !strings.Contains(plain, "Plan · Split auth") || !strings.Contains(plain, "2/2 done") {
		t.Fatalf("completed plan heading is wrong:\n%s", plain)
	}
	if !strings.Contains(plain, "✓ 1. read code") || !strings.Contains(plain, "- 2. old idea") {
		t.Fatalf("finished rows lost their marks:\n%s", plain)
	}
}

// A blocked step is waiting on the person: it keeps the warning mark and the
// reason the plan store recorded, not a fresh pending row.
func TestTrackerStripShowsBlockedReason(t *testing.T) {
	blocked := &todo.Item{
		ID:       "1",
		Subject:  "wire the API",
		Status:   todo.StatusPending,
		Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepBlocked), "the upstream API moved"),
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:     []*todo.Item{blocked},
		Width:     80,
		PlanTitle: "Split auth",
	}))
	if !strings.Contains(plain, kit.CurrentSymbols.Warning+" 1. wire the API") {
		t.Fatalf("blocked row lost its warning mark:\n%s", plain)
	}
	if !strings.Contains(plain, "the upstream API moved") {
		t.Fatalf("blocked row lost its reason:\n%s", plain)
	}
	if !strings.Contains(plain, "0/1 done") {
		t.Fatalf("blocked step should not count as done:\n%s", plain)
	}
}

// A plan's rows lead, and the session's own todos keep their own heading so
// they never read as plan steps.
func TestTrackerStripSeparatesPlanRowsFromOtherTodos(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read code", Status: todo.StatusInProgress,
			Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepInProgress), "")},
		{ID: "2", Subject: "Audit handlers", Status: todo.StatusPending},
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        80,
		PlanTitle:    "Split auth",
		Executing:    func(*todo.Item) bool { return true },
	}))
	planAt, todosAt, auditAt := strings.Index(plain, "Plan · Split auth"), strings.Index(plain, "Todos"), strings.Index(plain, "Audit handlers")
	if planAt < 0 || todosAt < 0 || auditAt < 0 {
		t.Fatalf("mixed list lost a heading:\n%s", plain)
	}
	if !(planAt < todosAt && todosAt < auditAt) {
		t.Fatalf("plan rows and session todos are not separated:\n%s", plain)
	}
}

func TestTrackerStripHeadingFitsNarrowTerminal(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read the whole authentication module", Status: todo.StatusInProgress,
			Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepInProgress), "")},
		{ID: "2", Subject: "wire the API", Status: todo.StatusPending,
			Metadata: todo.PlanMetadata("plan-1", "step-2", string(plan.StepBlocked), "the upstream API moved")},
	}
	for _, width := range []int{1, 4, 7, 12, 20, 40, 80} {
		plain := stripANSI(RenderTrackerList(TrackerListParams{
			Items:          items,
			Width:          width,
			PlanTitle:      "Split the authentication module end to end",
			PlanStatusNote: "2 blocked steps need you",
			Paused:         true,
			Awaiting:       true,
			Executing:      func(*todo.Item) bool { return true },
		}))
		for line := range strings.SplitSeq(strings.TrimSuffix(plain, "\n"), "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("width %d: strip overflowed: %q", width, line)
			}
		}
	}
}

func TestRenderTrackerListHeadingFitsNarrowTerminal(t *testing.T) {
	worker := &todo.Item{Subject: "Compress video", Status: todo.StatusPending,
		Metadata: map[string]any{"background_task_id": "bg-1"}}
	plan := &todo.Item{ID: "1", Subject: "Write tests", Status: todo.StatusCompleted}
	for _, items := range [][]*todo.Item{{worker}, {worker, plan}} {
		for _, width := range []int{1, 4, 7, 12, 80} {
			plain := stripANSI(RenderTrackerList(TrackerListParams{Items: items, StreamActive: true, Width: width}))
			for line := range strings.SplitSeq(strings.TrimSuffix(plain, "\n"), "\n") {
				if lipgloss.Width(line) > width {
					t.Fatalf("tracker exceeded %d columns:\n%s", width, plain)
				}
			}
		}
	}
}

func TestRenderTrackerListRetiresCompletedWorkerWithoutDeletingIt(t *testing.T) {
	store := todo.NewStore()
	done := store.Create("Upload finished", "", "", map[string]any{"background_task_id": "bg-done"})
	if err := store.Update(done.ID, todo.WithStatus(todo.StatusCompleted)); err != nil {
		t.Fatal(err)
	}
	store.Create("Compress video", "", "", map[string]any{"background_task_id": "bg-active"})
	for _, streaming := range []bool{false, true} {
		plain := stripANSI(RenderTrackerList(TrackerListParams{Items: store.List(), StreamActive: streaming, Width: 80}))
		if strings.Contains(plain, "Upload finished") || strings.Contains(plain, "completed") || !strings.Contains(plain, "Compress video") {
			t.Fatalf("successful worker should leave without a history summary:\n%s", plain)
		}
	}
	if items := store.List(); len(items) != 2 || items[0].Status != todo.StatusCompleted {
		t.Fatalf("rendering changed task records: %+v", items)
	}
}

func TestRenderTrackerRowReservesSpaceForStatus(t *testing.T) {
	for _, subject := range []string{strings.Repeat("Compress video ", 10), strings.Repeat("压缩视频", 10)} {
		item := &todo.Item{
			Subject: subject, ActiveForm: subject, Status: todo.StatusInProgress,
			StatusChangedAt: time.Now().Add(-70 * time.Second),
			Metadata:        map[string]any{"background_task_id": "bg-1", "background_status_detail": "failed"},
		}
		for _, tc := range []struct {
			phase  itemPhase
			detail string
		}{
			{itemRunning, "· 1m "},
			{itemStalled, "[stalled]"},
			{itemAborted, "[failed]"},
		} {
			for _, width := range []int{1, 12, 40, 80} {
				plain := stripANSI(renderItem(item, tc.phase, width, itemNumber{width: 3}, nil, nil, nil))
				if lipgloss.Width(plain) > width || strings.Count(plain, "\n") != 1 {
					t.Fatalf("row exceeded %d columns:\n%s", width, plain)
				}
				if width >= 40 && (!strings.Contains(plain, "…") || !strings.Contains(plain, tc.detail)) {
					t.Fatalf("long description hid %q:\n%s", tc.detail, plain)
				}
			}
		}
	}
}

// ordinalTagRe captures a row's ordinal tag ("12.") from rendered output.
var ordinalTagRe = regexp.MustCompile(`\b(\d+)\.`)

// Numbers run through the whole strip: the plan's steps keep 1..N — the same
// numbers the Execution Panel shows for the same steps — and the session's own
// todos continue the sequence, even when a todo was created first.
func TestTrackerStripNumbersPlanThenTodos(t *testing.T) {
	items := []*todo.Item{
		{ID: "7", Subject: "Audit handlers", Status: todo.StatusPending},
		{ID: "8", Subject: "read code", Status: todo.StatusCompleted,
			Metadata: todo.PlanMetadata("plan-1", "step-1", string(plan.StepCompleted), "")},
		{ID: "9", Subject: "move files", Status: todo.StatusInProgress,
			Metadata: todo.PlanMetadata("plan-1", "step-2", string(plan.StepInProgress), "")},
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        80,
		PlanTitle:    "Split auth",
		Executing:    func(*todo.Item) bool { return true },
	}))

	last := -1
	for _, want := range []string{"1. read code", "2. move files", "3. Audit handlers"} {
		at := strings.Index(plain, want)
		if at < 0 {
			t.Fatalf("row %q missing:\n%s", want, plain)
		}
		if at < last {
			t.Fatalf("row %q is out of reading order:\n%s", want, plain)
		}
		last = at
	}
	if taskIDRe.MatchString(plain) {
		t.Fatalf("a row still shows a todo id as its tag:\n%s", plain)
	}
}

// A finished row keeps the number it had and is struck through, so the list
// stays a record of what ran rather than renumbering around it.
func TestTrackerStripKeepsNumbersOnFinishedRows(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read code", Status: todo.StatusCompleted},
		{ID: "2", Subject: "move files", Status: todo.StatusPending},
	}
	view := RenderTrackerList(TrackerListParams{Items: items, StreamActive: true, Width: 80})
	plain := stripANSI(view)
	if !strings.Contains(plain, "✓ 1. read code") || !strings.Contains(plain, "○ 2. move files") {
		t.Fatalf("ordinals were renumbered around finished work:\n%s", plain)
	}
	struck := func(label string) bool {
		for _, line := range strings.Split(view, "\n") {
			if strings.Contains(line, label) {
				return strings.Contains(line, sgrStrikeOn)
			}
		}
		t.Fatalf("row %q missing:\n%s", label, plain)
		return false
	}
	if !struck("read code") {
		t.Fatalf("finished row is not struck through:\n%s", plain)
	}
	if struck("move files") {
		t.Fatalf("pending row is struck through:\n%s", plain)
	}
}

// Numbers are assigned to the whole list before it is windowed, so a folded
// row leaves a gap rather than renumbering the rows still on screen.
func TestTrackerStripKeepsNumbersWhenRowsFold(t *testing.T) {
	items := make([]*todo.Item, 0, maxVisibleItems+3)
	for i := 1; i <= maxVisibleItems+3; i++ {
		status := todo.StatusPending
		switch {
		case i <= maxVisibleItems:
			status = todo.StatusCompleted
		case i == maxVisibleItems+1:
			status = todo.StatusInProgress
		}
		items = append(items, &todo.Item{ID: strconv.Itoa(i), Subject: "Task " + strconv.Itoa(i), Status: status})
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        80,
		Executing:    func(*todo.Item) bool { return true },
	}))

	if !strings.Contains(plain, "3 completed") {
		t.Fatalf("the leading finished rows did not fold away:\n%s", plain)
	}
	got := numbersOf(ordinalTagRe.FindAllStringSubmatch(plain, -1))
	want := []string{"4", "5", "6", "7", "8", "9", "10", "11"}
	if !slices.Equal(got, want) {
		t.Fatalf("visible ordinals:\n  got:  %v\n  want: %v\n\nfull output:\n%s", got, want, plain)
	}
}

// Background work is nobody's step: it takes no ordinal, but it keeps the
// column so its label lines up with the numbered rows around it.
func TestTrackerStripAlignsUnnumberedWorkerRows(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read code", Status: todo.StatusPending},
		{ID: "2", Subject: "Upload video", Status: todo.StatusInProgress,
			Metadata: map[string]any{"background_task_id": "bg-1"}},
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        80,
		Executing:    func(*todo.Item) bool { return true },
	}))

	column := func(label string) int {
		for _, line := range strings.Split(plain, "\n") {
			if at := strings.Index(line, label); at >= 0 {
				return lipgloss.Width(line[:at])
			}
		}
		t.Fatalf("row %q missing:\n%s", label, plain)
		return 0
	}
	if got, want := column("Upload video"), column("read code"); got != want {
		t.Fatalf("worker label starts at column %d, numbered row at %d:\n%s", got, want, plain)
	}
	if strings.Contains(plain, "1. Upload video") || strings.Contains(plain, "2. Upload video") {
		t.Fatalf("a background worker took an ordinal:\n%s", plain)
	}
}

// Nothing on the strip animates: identical state renders the same bytes, so the
// list a person is reading does not blink.
func TestTrackerStripDoesNotBlink(t *testing.T) {
	items := []*todo.Item{{ID: "1", Subject: "move files", Status: todo.StatusInProgress}}
	render := func() string {
		return RenderTrackerList(TrackerListParams{
			Items:        items,
			StreamActive: true,
			Width:        80,
			Executing:    func(*todo.Item) bool { return true },
		})
	}

	first := render()
	if !strings.Contains(stripANSI(first), kit.CurrentSymbols.LiveDot) {
		t.Fatalf("the running row lost its mark:\n%s", stripANSI(first))
	}
	for range 8 {
		if again := render(); again != first {
			t.Fatalf("the strip changed between identical renders:\n%q\n%q", first, again)
		}
	}
}

// A blocked row points at the number a reader can find on the list, and keeps
// the id only for work that is not on screen.
func TestTrackerStripNamesBlockersByOrdinal(t *testing.T) {
	items := []*todo.Item{
		{ID: "1", Subject: "read code", Status: todo.StatusInProgress},
		{ID: "2", Subject: "move files", Status: todo.StatusPending},
		{ID: "3", Subject: "ship it", Status: todo.StatusPending},
	}
	blockers := func(id string) []string {
		if id == "2" {
			return []string{"1", "99"}
		}
		return nil
	}
	plain := stripANSI(RenderTrackerList(TrackerListParams{
		Items:        items,
		StreamActive: true,
		Width:        80,
		Blockers:     blockers,
		Executing:    func(*todo.Item) bool { return true },
	}))

	if !strings.Contains(plain, "← 1, #99") {
		t.Fatalf("blocker references are not ordinals:\n%s", plain)
	}
}
