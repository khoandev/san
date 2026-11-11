package todo

// Keys joining a tracker item to a Plan Mode step. The plan package stays the
// source of truth for a step's lifecycle; these fields are the projection the
// app keeps in step with, so an executing plan reads as a real todo list above
// the composer instead of a second, plan-only progress row.
const (
	// MetadataPlanID names the plan a projected item belongs to.
	MetadataPlanID = "plan_id"
	// MetadataPlanStepID joins an item to one plan step. It is the lookup key
	// the projection uses to stay idempotent across syncs and a resume.
	MetadataPlanStepID = "plan_step_id"
	// metadataPlanStepStatus records the plan's own step status, including
	// blocked and skipped, which the item statuses cannot express.
	metadataPlanStepStatus      = "plan_step_status"
	metadataPlanStepBlockReason = "plan_step_block_reason"
)

// PlanMetadata builds the metadata a projected plan step carries. An empty
// block reason is written as nil, which WithMetadata deletes, so a step that
// moved on does not keep the reason it was blocked for.
func PlanMetadata(planID, stepID, stepStatus, blockReason string) map[string]any {
	metadata := map[string]any{
		MetadataPlanID:     planID,
		MetadataPlanStepID: stepID,
	}
	if stepStatus == "" {
		metadata[metadataPlanStepStatus] = nil
	} else {
		metadata[metadataPlanStepStatus] = stepStatus
	}
	if blockReason == "" {
		metadata[metadataPlanStepBlockReason] = nil
	} else {
		metadata[metadataPlanStepBlockReason] = blockReason
	}
	return metadata
}

// PlanID returns the plan a projected item belongs to, or "" for a todo the
// model authored itself.
func PlanID(item *Item) string { return metadataString(item, MetadataPlanID) }

// PlanStepID returns the plan step a projected item mirrors, or "".
func PlanStepID(item *Item) string { return metadataString(item, MetadataPlanStepID) }

// PlanStepStatus returns the plan step's own status — including "blocked" and
// "skipped", which the item status cannot carry — or "" for an ordinary todo.
func PlanStepStatus(item *Item) string { return metadataString(item, metadataPlanStepStatus) }

// PlanStepBlockReason returns why a projected step is blocked, or "".
func PlanStepBlockReason(item *Item) string {
	return metadataString(item, metadataPlanStepBlockReason)
}
