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

type WorkflowTrigger struct {
	ID          WorkflowTriggerID
	WorkflowID  WorkflowID
	WorkspaceID WorkspaceID
	AppID       AppID
	Title       string
	Type        string
	Config      string
	Enabled     bool
	Version     uint64
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
