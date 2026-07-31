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
