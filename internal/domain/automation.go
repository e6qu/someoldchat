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
	ManagerIDs       []UserID
	CallbackID       string
	Title            string
	Description      string
	Icon             string
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
	Icon        string
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
	Kind   WorkflowStepType
	Title  string
	Label  string
	Fields []WorkflowInteractionField
}

// WorkflowInteractionField is one named input a form step collects.
type WorkflowInteractionField struct {
	Name  string
	Label string
}

// WorkflowFormResponse is one submitted field of one form step in one run: the
// unit a form-response CSV export is built from.
type WorkflowFormResponse struct {
	RunID           WorkflowRunID
	WorkflowVersion uint64
	FormTitle       string
	Field           string
	Value           string
	SubmittedAt     time.Time
}

// WorkflowStepType is what one workflow step does. The service held these as
// untyped constants while the web builder spelt the same eight values as string
// literals in its own switch, so a typo on either side fell through to the
// default and the step silently did nothing. One type, named once.
type WorkflowStepType string

const (
	WorkflowStepFunction  WorkflowStepType = "function"
	WorkflowStepForm      WorkflowStepType = "form"
	WorkflowStepButton    WorkflowStepType = "button"
	WorkflowStepMessage   WorkflowStepType = "message"
	WorkflowStepAddPeople WorkflowStepType = "add_people"
	WorkflowStepCanvas    WorkflowStepType = "create_canvas"
	WorkflowStepDelay     WorkflowStepType = "delay"
	WorkflowStepWaitUntil WorkflowStepType = "wait_until"
)

// Valid accepts the eight kinds. The empty value is not one of them: a step
// with no kind is read as a function step by the decoder, and OrFunction says
// so where that is meant rather than leaving it implicit.
func (kind WorkflowStepType) Valid() bool {
	switch kind {
	case WorkflowStepFunction, WorkflowStepForm, WorkflowStepButton, WorkflowStepMessage,
		WorkflowStepAddPeople, WorkflowStepCanvas, WorkflowStepDelay, WorkflowStepWaitUntil:
		return true
	}
	return false
}

// OrFunction reads an unset kind as the function step, which is the default a
// step with no type has always had.
func (kind WorkflowStepType) OrFunction() WorkflowStepType {
	if kind == "" {
		return WorkflowStepFunction
	}
	return kind
}

// BuiltIn reports whether the run performs this step itself. Every other kind
// ends when something external arrives.
func (kind WorkflowStepType) BuiltIn() bool {
	switch kind {
	case WorkflowStepMessage, WorkflowStepAddPeople, WorkflowStepCanvas:
		return true
	}
	return false
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

// Valid reports whether this is a trigger type the platform runs. The field on
// WorkflowTrigger used to be a bare string, so a misspelt type stored cleanly
// and then matched no dispatcher: the trigger existed and never fired.
func (kind WorkflowTriggerType) Valid() bool {
	switch kind {
	case WorkflowTriggerLink, WorkflowTriggerShortcut, WorkflowTriggerScheduled, WorkflowTriggerWebhook,
		WorkflowTriggerMessage, WorkflowTriggerReaction, WorkflowTriggerJoin, WorkflowTriggerList:
		return true
	}
	return false
}

// WorkflowTriggerTypes is every trigger type, in a stable order.
func WorkflowTriggerTypes() []WorkflowTriggerType {
	return []WorkflowTriggerType{
		WorkflowTriggerLink, WorkflowTriggerShortcut, WorkflowTriggerScheduled, WorkflowTriggerWebhook,
		WorkflowTriggerMessage, WorkflowTriggerReaction, WorkflowTriggerJoin, WorkflowTriggerList,
	}
}

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
	Type        WorkflowTriggerType
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
	// A run is created already running: runWorkflow, the one path every
	// interactive and automatic trigger funnels through, sets it. There is no
	// worker that picks a queued run up, so "queued" was a status nothing could
	// produce. It is gone rather than declared-and-unreachable.
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

// PermissionType is who may exercise one automation. Slack names four values
// and this used to be an untyped string in the domain, the store, the seam and
// the handler, so a misspelling read as "nobody but the collaborators" - the
// safest-looking answer and the wrong one.
type PermissionType string

const (
	PermissionEveryone         PermissionType = "everyone"
	PermissionAppCollaborators PermissionType = "app_collaborators"
	PermissionNamedEntities    PermissionType = "named_entities"
	PermissionNoOne            PermissionType = "no_one"
	// Slack sets a trigger's permission to system when the platform owns it.
	PermissionSystem PermissionType = "system"
)

func (permission PermissionType) Valid() bool {
	switch permission {
	case PermissionEveryone, PermissionAppCollaborators, PermissionNamedEntities, PermissionNoOne, PermissionSystem:
		return true
	}
	return false
}

// SettableBy reports whether an administrator may set this value on a resource.
// system is the platform's own answer and never an administrator's.
func (permission PermissionType) SettableBy() bool {
	return permission.Valid() && permission != PermissionSystem
}

type AutomationPermission struct {
	ResourceType   string
	ResourceID     string
	WorkspaceID    WorkspaceID
	AppID          AppID
	PermissionType PermissionType
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
