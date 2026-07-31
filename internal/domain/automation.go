package domain

import "time"

type WorkflowStatus string

const (
	WorkflowDraft     WorkflowStatus = "draft"
	WorkflowPublished WorkflowStatus = "published"
	WorkflowDisabled  WorkflowStatus = "disabled"
)

type WorkflowDefinition struct {
	ID               WorkflowID
	WorkspaceID      WorkspaceID
	AppID            AppID
	OwnerID          UserID
	CallbackID       string
	Title            string
	Description      string
	InputSchema      string
	Steps            string
	Status           WorkflowStatus
	Version          uint64
	PublishedVersion uint64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type WorkflowRevision struct {
	WorkflowID  WorkflowID
	WorkspaceID WorkspaceID
	Version     uint64
	Title       string
	Description string
	CallbackID  string
	InputSchema string
	Steps       string
	Status      WorkflowStatus
	CreatedAt   time.Time
}

type WorkflowStepVersion struct {
	Title                  string     `json:"title"`
	WorkflowID             WorkflowID `json:"workflow_id"`
	StepID                 string     `json:"step_id"`
	IsDeleted              bool       `json:"is_deleted"`
	WorkflowVersionCreated string     `json:"workflow_version_created"`
}

type WorkflowStepChangeType string

const (
	// WorkflowStepChangeAdded marks a step slot that exists in the staged draft
	// but not in the published revision.
	WorkflowStepChangeAdded WorkflowStepChangeType = "added"
	// WorkflowStepChangeChanged marks a step slot whose function differs from
	// the published revision's step at the same position.
	WorkflowStepChangeChanged WorkflowStepChangeType = "changed"
	// WorkflowStepChangeRemoved marks a step slot that exists in the published
	// revision but no longer in the staged draft.
	WorkflowStepChangeRemoved WorkflowStepChangeType = "removed"
)

// WorkflowStepChange is one step-level difference between a published
// workflow's staged draft and its published revision. Position is one-based and
// names the head slot the difference applies to, which is how the builder
// renders its step list.
type WorkflowStepChange struct {
	Position   int
	FunctionID string
	Change     WorkflowStepChangeType
}

// WorkflowActivity is the run dashboard for one workflow: a count per
// terminal and in-flight status plus the most recent runs, newest first.
// Slack's builder shows the same activity view to the workflow's managers.
type WorkflowActivity struct {
	Queued     int
	Running    int
	Completed  int
	Failed     int
	Cancelled  int
	RecentRuns []WorkflowRun
}

// WorkflowInteraction describes the human input a running workflow is parked
// on, if any. Kind is empty when the run is not waiting on a form or button.
type WorkflowInteraction struct {
	StepID WorkflowStepID
	Kind   string
	Title  string
	Label  string
	Fields []WorkflowInteractionField
}

// WorkflowInteractionField is one named input a form step collects.
type WorkflowInteractionField struct {
	Name  string
	Label string
}

type WorkflowTriggerType string

const (
	WorkflowTriggerLink      WorkflowTriggerType = "link"
	WorkflowTriggerShortcut  WorkflowTriggerType = "shortcut"
	WorkflowTriggerScheduled WorkflowTriggerType = "scheduled"
	WorkflowTriggerWebhook   WorkflowTriggerType = "webhook"
	WorkflowTriggerMessage   WorkflowTriggerType = "message"
	WorkflowTriggerReaction  WorkflowTriggerType = "reaction"
	WorkflowTriggerJoin      WorkflowTriggerType = "join"
	WorkflowTriggerList      WorkflowTriggerType = "list"
)

// EventWorkflowTriggerTypes are the trigger types a workspace event dispatcher
// fires. Scheduled and webhook triggers have their own execution paths.
var EventWorkflowTriggerTypes = []WorkflowTriggerType{
	WorkflowTriggerMessage, WorkflowTriggerReaction, WorkflowTriggerJoin, WorkflowTriggerList,
}

type WorkflowTrigger struct {
	ID          WorkflowTriggerID
	WorkflowID  WorkflowID
	WorkspaceID WorkspaceID
	AppID       AppID
	Title       string
	Type        string
	Config      string
	Enabled     bool
	// NextRunAt is the next scheduled fire time for a scheduled trigger. It is
	// zero for every other type, and the schedule worker's compare-and-set
	// fence: editing the trigger replaces it, so a worker that read the old
	// value cannot fire a superseded schedule.
	NextRunAt time.Time
	Version   uint64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type WorkflowRunStatus string

const (
	WorkflowRunQueued    WorkflowRunStatus = "queued"
	WorkflowRunRunning   WorkflowRunStatus = "running"
	WorkflowRunCompleted WorkflowRunStatus = "completed"
	WorkflowRunFailed    WorkflowRunStatus = "failed"
	WorkflowRunCancelled WorkflowRunStatus = "cancelled"
)

type WorkflowRun struct {
	ID              WorkflowRunID
	WorkflowID      WorkflowID
	WorkflowVersion uint64
	TriggerID       WorkflowTriggerID
	WorkspaceID     WorkspaceID
	AppID           AppID
	ActorID         UserID
	ConversationID  ConversationID
	Status          WorkflowRunStatus
	Inputs          string
	Outputs         string
	Error           string
	CurrentStep     int
	IdempotencyKey  string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     time.Time
}

type AutomationPermission struct {
	ResourceType   string
	ResourceID     string
	WorkspaceID    WorkspaceID
	AppID          AppID
	PermissionType string
	UserIDs        []UserID
	ChannelIDs     []ConversationID
	TeamIDs        []WorkspaceID
	OrgIDs         []string
	UpdatedAt      time.Time
}

type FeaturedWorkflow struct {
	WorkspaceID    WorkspaceID
	ConversationID ConversationID
	TriggerID      WorkflowTriggerID
	Title          string
	Position       int
}
