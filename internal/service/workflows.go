package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type workflowFunctionDefinition struct {
	ID           string                 `json:"id,omitempty"`
	Type         string                 `json:"type,omitempty"`
	FunctionID   string                 `json:"function_id"`
	AppID        domain.AppID           `json:"app_id,omitempty"`
	Title        string                 `json:"title,omitempty"`
	Inputs       any                    `json:"inputs,omitempty"`
	InputMapping map[string]string      `json:"input_mapping,omitempty"`
	Condition    *workflowStepCondition `json:"condition,omitempty"`
	Form         *workflowFormStep      `json:"form,omitempty"`
	Button       *workflowButtonStep    `json:"button,omitempty"`
}

// workflowFormStep describes a step that pauses the run until someone fills in
// and submits a form. Slack's built-in form step collects named fields.
type workflowFormStep struct {
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	Inputs      map[string]string `json:"inputs,omitempty"`
}

// workflowButtonStep describes a step that pauses the run until someone clicks
// a confirmation button, Slack's built-in approval step.
type workflowButtonStep struct {
	Label string `json:"label"`
}

// workflowStepCondition gates one step on a variable comparison, Slack's
// conditional logic for a flat step list: the step runs only when the
// comparison holds against the run's inputs or an earlier step's outputs.
type workflowStepCondition struct {
	Source   string `json:"source"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// workflowStepTypeFunction is the default step kind: an app function callback.
// Form and button steps are additional kinds with their own execution model.
const workflowStepTypeFunction = "function"

const (
	workflowStepTypeForm   = "form"
	workflowStepTypeButton = "button"
)

// workflowConditionOperators are the comparisons a step condition supports,
// matching the operators Slack's conditional branch editor offers.
var workflowConditionOperators = map[string]bool{
	"equals": true, "not_equals": true, "contains": true, "greater_than": true, "less_than": true,
}

type functionExecutionSnapshot struct {
	AppID               domain.AppID          `json:"app_id"`
	FunctionExecutionID domain.WorkflowStepID `json:"function_execution_id"`
	Function            json.RawMessage       `json:"function"`
	WorkflowRunID       domain.WorkflowRunID  `json:"workflow_execution_id"`
	Inputs              json.RawMessage       `json:"inputs"`
}

type slackFunctionSnapshot struct {
	ID               string                          `json:"id"`
	CallbackID       string                          `json:"callback_id"`
	Title            string                          `json:"title"`
	Description      string                          `json:"description,omitempty"`
	Type             string                          `json:"type"`
	InputParameters  []appmanifest.FunctionParameter `json:"input_parameters"`
	OutputParameters []appmanifest.FunctionParameter `json:"output_parameters"`
	AppID            domain.AppID                    `json:"app_id"`
	DateCreated      int64                           `json:"date_created"`
	DateUpdated      int64                           `json:"date_updated"`
	DateDeleted      int64                           `json:"date_deleted"`
	Runtime          string                          `json:"-"`
}

func normalizeJSONObject(raw string, allowEmpty bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && allowEmpty {
		return "{}", nil
	}
	var value map[string]json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return "", ErrInvalidWorkflowStep
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func normalizeWorkflowSteps(raw string) (string, []workflowFunctionDefinition, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "[]"
	}
	var values []workflowFunctionDefinition
	if json.Unmarshal([]byte(raw), &values) != nil || values == nil {
		return "", nil, ErrInvalidWorkflowStep
	}
	seen := make(map[string]int, len(values))
	for index := range values {
		values[index].ID = strings.TrimSpace(values[index].ID)
		values[index].Type = strings.TrimSpace(values[index].Type)
		values[index].FunctionID = strings.TrimSpace(values[index].FunctionID)
		values[index].Title = strings.TrimSpace(values[index].Title)
		if values[index].Type == "" {
			values[index].Type = workflowStepTypeFunction
		}
		switch values[index].Type {
		case workflowStepTypeFunction:
			if values[index].FunctionID == "" {
				return "", nil, ErrInvalidWorkflowStep
			}
		case workflowStepTypeForm:
			if values[index].Form == nil || strings.TrimSpace(values[index].Form.Title) == "" || values[index].FunctionID != "" {
				return "", nil, ErrInvalidWorkflowStep
			}
			values[index].Form.Title = strings.TrimSpace(values[index].Form.Title)
			values[index].Form.Description = strings.TrimSpace(values[index].Form.Description)
			for name, label := range values[index].Form.Inputs {
				if strings.TrimSpace(name) == "" || strings.TrimSpace(label) == "" {
					return "", nil, ErrInvalidWorkflowStep
				}
				values[index].Form.Inputs[name] = strings.TrimSpace(label)
			}
		case workflowStepTypeButton:
			if values[index].Button == nil || strings.TrimSpace(values[index].Button.Label) == "" || values[index].FunctionID != "" {
				return "", nil, ErrInvalidWorkflowStep
			}
			values[index].Button.Label = strings.TrimSpace(values[index].Button.Label)
		default:
			return "", nil, ErrInvalidWorkflowStep
		}
		// Step ids route branches and variable references, so they must be
		// unique. An explicit duplicate is a definition error; a defaulted id
		// colliding with an earlier step is suffixed, which keeps existing
		// workflows that repeat one function valid.
		if values[index].ID == "" {
			values[index].ID = values[index].FunctionID
			for suffix := 2; ; suffix++ {
				if _, taken := seen[values[index].ID]; !taken {
					break
				}
				values[index].ID = fmt.Sprintf("%s-%d", values[index].FunctionID, suffix)
			}
		} else if _, taken := seen[values[index].ID]; taken {
			return "", nil, ErrInvalidWorkflowStep
		}
		if condition := values[index].Condition; condition != nil {
			condition.Source = strings.TrimSpace(condition.Source)
			condition.Operator = strings.TrimSpace(condition.Operator)
			if !workflowConditionOperators[condition.Operator] {
				return "", nil, ErrInvalidWorkflowStep
			}
			source, err := parseWorkflowVariableSource(condition.Source)
			if err != nil {
				return "", nil, err
			}
			// A condition may only read an earlier step's outputs: execution
			// is sequential, so a forward or self reference could never
			// resolve.
			if source.Kind == "steps" {
				if _, defined := seen[source.StepID]; !defined {
					return "", nil, ErrInvalidWorkflowStep
				}
			}
		}
		seen[values[index].ID] = index
	}
	// Input mappings follow the same reference rules as conditions: a
	// variable source must parse, and a step output may only come from an
	// earlier step. A value without a variable prefix is a literal.
	for index := range values {
		for name, target := range values[index].InputMapping {
			if strings.TrimSpace(name) == "" {
				return "", nil, ErrInvalidWorkflowStep
			}
			target = strings.TrimSpace(target)
			if !strings.HasPrefix(target, "inputs.") && !strings.HasPrefix(target, "steps.") {
				continue
			}
			source, err := parseWorkflowVariableSource(target)
			if err != nil {
				return "", nil, err
			}
			if source.Kind == "steps" {
				if earlier, defined := seen[source.StepID]; !defined || earlier >= index {
					return "", nil, ErrInvalidWorkflowStep
				}
			}
			values[index].InputMapping[name] = target
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), values, nil
}

// workflowVariableSource is one parsed variable reference: a run input
// (`inputs.<name>`) or an earlier step's output (`steps.<id>.outputs.<name>`).
type workflowVariableSource struct {
	Kind     string
	StepID   string
	Property string
}

func parseWorkflowVariableSource(raw string) (workflowVariableSource, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "inputs."):
		property := strings.TrimPrefix(raw, "inputs.")
		if property == "" || strings.Contains(property, ".") {
			return workflowVariableSource{}, ErrInvalidWorkflowStep
		}
		return workflowVariableSource{Kind: "inputs", Property: property}, nil
	case strings.HasPrefix(raw, "steps."):
		parts := strings.Split(strings.TrimPrefix(raw, "steps."), ".")
		if len(parts) != 3 || parts[0] == "" || parts[1] != "outputs" || parts[2] == "" {
			return workflowVariableSource{}, ErrInvalidWorkflowStep
		}
		return workflowVariableSource{Kind: "steps", StepID: parts[0], Property: parts[2]}, nil
	default:
		return workflowVariableSource{}, ErrInvalidWorkflowStep
	}
}

// resolveWorkflowVariable reads one variable from the run's inputs or a
// completed step's outputs. The second return is false when the property does
// not exist, which conditions treat differently from an empty value.
func resolveWorkflowVariable(source workflowVariableSource, inputs map[string]any, outputs map[string]map[string]any) (any, bool) {
	switch source.Kind {
	case "inputs":
		value, found := inputs[source.Property]
		return value, found
	case "steps":
		stepOutputs, found := outputs[source.StepID]
		if !found {
			return nil, false
		}
		value, found := stepOutputs[source.Property]
		return value, found
	}
	return nil, false
}

// workflowVariableText renders a variable for comparison: strings as
// themselves, JSON numbers without trailing zeros, booleans as words, and
// anything structured as its JSON encoding.
func workflowVariableText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case nil:
		return ""
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// workflowConditionHolds evaluates one step condition against the run's
// variables. Numeric operators require both sides to parse as numbers; a
// missing variable makes every comparison false except not_equals, where a
// missing variable is by definition not equal to the expected value.
func workflowConditionHolds(condition *workflowStepCondition, inputs map[string]any, outputs map[string]map[string]any) bool {
	if condition == nil {
		return true
	}
	source, err := parseWorkflowVariableSource(condition.Source)
	if err != nil {
		return false
	}
	actual, found := resolveWorkflowVariable(source, inputs, outputs)
	switch condition.Operator {
	case "equals":
		return found && workflowVariableText(actual) == condition.Value
	case "not_equals":
		return !found || workflowVariableText(actual) != condition.Value
	case "contains":
		return found && strings.Contains(workflowVariableText(actual), condition.Value)
	case "greater_than", "less_than":
		if !found {
			return false
		}
		left, err := strconv.ParseFloat(workflowVariableText(actual), 64)
		if err != nil {
			return false
		}
		right, err := strconv.ParseFloat(condition.Value, 64)
		if err != nil {
			return false
		}
		if condition.Operator == "greater_than" {
			return left > right
		}
		return left < right
	}
	return false
}

// firstRunnableWorkflowStep returns the index of the first step whose
// condition holds, or -1 when every remaining step is skipped. Execution uses
// it both at run start and after each step completes.
func firstRunnableWorkflowStep(steps []workflowFunctionDefinition, from int, inputs map[string]any, outputs map[string]map[string]any) int {
	for index := max(from, 0); index < len(steps); index++ {
		if workflowConditionHolds(steps[index].Condition, inputs, outputs) {
			return index
		}
	}
	return -1
}

// decodeWorkflowInputsMap parses a run's inputs JSON for variable resolution.
// Inputs were normalized on the way in, so a parse failure means an empty
// object rather than an error path.
func decodeWorkflowInputsMap(raw string) map[string]any {
	values := map[string]any{}
	if json.Unmarshal([]byte(raw), &values) != nil {
		return map[string]any{}
	}
	return values
}

// workflowStepOutputsByID collects completed executions' outputs keyed by
// their step's id, which is what `steps.<id>.outputs.<name>` resolves
// against.
func workflowStepOutputsByID(executions []domain.WorkflowStep) map[string]map[string]any {
	outputs := make(map[string]map[string]any, len(executions))
	for _, execution := range executions {
		if execution.Status != domain.WorkflowStepCompleted {
			continue
		}
		values := map[string]any{}
		if json.Unmarshal([]byte(execution.Outputs), &values) != nil {
			continue
		}
		outputs[execution.EditID] = values
	}
	return outputs
}

func (m Messages) CreateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, value domain.WorkflowDefinition) (domain.WorkflowDefinition, error) {
	if _, _, err := m.GetDeveloperApp(ctx, workspaceID, actor, value.AppID); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	value.Title = strings.TrimSpace(value.Title)
	value.Description = strings.TrimSpace(value.Description)
	value.CallbackID = strings.TrimSpace(value.CallbackID)
	if value.Title == "" {
		return domain.WorkflowDefinition{}, ErrInvalidWorkflowStep
	}
	inputSchema, err := normalizeJSONObject(value.InputSchema, true)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	steps, stepValues, err := normalizeWorkflowSteps(value.Steps)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := m.validateWorkflowFunctions(ctx, value.AppID, stepValues); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	id, err := domain.NewWorkflowID()
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	now := time.Now().UTC()
	value.ID = id
	value.WorkspaceID = workspaceID
	value.OwnerID = actor
	value.InputSchema = inputSchema
	value.Steps = steps
	value.Status = domain.WorkflowDraft
	value.Version = 1
	value.PublishedVersion = 0
	value.CreatedAt = now
	value.UpdatedAt = now
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.created",
		events.String("workflow_id", string(value.ID)),
		events.String("app_id", string(value.AppID)),
	), now)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := m.Store.CreateWorkflow(ctx, value, event); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return value, nil
}

func (m Messages) UpdateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, value domain.WorkflowDefinition, expectedVersion uint64, publish bool) (domain.WorkflowDefinition, error) {
	current, err := m.Store.GetWorkflow(ctx, workspaceID, value.ID)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if current.OwnerID != actor {
		return domain.WorkflowDefinition{}, store.ErrNotFound
	}
	if _, _, err := m.GetDeveloperApp(ctx, workspaceID, actor, current.AppID); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	unpublish := !publish && value.Status == domain.WorkflowDisabled
	value.Title = strings.TrimSpace(value.Title)
	value.Description = strings.TrimSpace(value.Description)
	value.CallbackID = strings.TrimSpace(value.CallbackID)
	if value.Title == "" || value.AppID != "" && value.AppID != current.AppID {
		return domain.WorkflowDefinition{}, ErrInvalidWorkflowStep
	}
	inputSchema, err := normalizeJSONObject(value.InputSchema, true)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	steps, stepValues, err := normalizeWorkflowSteps(value.Steps)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if publish && len(stepValues) == 0 {
		return domain.WorkflowDefinition{}, ErrInvalidWorkflowStep
	}
	if err := m.validateWorkflowFunctions(ctx, current.AppID, stepValues); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	value.ID = current.ID
	value.WorkspaceID = current.WorkspaceID
	value.AppID = current.AppID
	value.OwnerID = current.OwnerID
	value.InputSchema = inputSchema
	value.Steps = steps
	value.CreatedAt = current.CreatedAt
	value.UpdatedAt = time.Now().UTC()
	value.Version = expectedVersion + 1
	value.PublishedVersion = current.PublishedVersion
	// A staged edit to a published workflow keeps the published revision live:
	// the head row carries the draft, runs keep pinning PublishedVersion, and
	// Version diverging from PublishedVersion is the marker that staged edits
	// exist. Unpublish takes the workflow offline; every other edit leaves a
	// draft or published head exactly as the caller's action says.
	value.Status = domain.WorkflowDraft
	topic := "workflow.updated"
	switch {
	case publish:
		value.Status = domain.WorkflowPublished
		value.PublishedVersion = expectedVersion + 1
		topic = "workflow.published"
	case unpublish:
		value.Status = domain.WorkflowDisabled
		topic = "workflow.unpublished"
	case current.Status == domain.WorkflowPublished:
		// Stage the edit without taking the published revision offline.
		value.Status = domain.WorkflowPublished
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload(topic,
		events.String("workflow_id", string(value.ID)),
		events.String("app_id", string(value.AppID)),
		events.Int("version", int64(value.Version)),
	), value.UpdatedAt)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if err := m.Store.UpdateWorkflow(ctx, value, expectedVersion, event); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	return value, nil
}

// DiscardWorkflowStagedChanges reverts a published workflow's draft to its
// published revision. The owner reads the live (staged) head; this operation
// throws those staged edits away and realigns the head to what is currently
// running, so the builder no longer reports pending changes.
func (m Messages) DiscardWorkflowStagedChanges(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID, expectedVersion uint64) error {
	current, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return err
	}
	if current.OwnerID != actor {
		return store.ErrNotFound
	}
	if current.Status != domain.WorkflowPublished || current.Version == current.PublishedVersion {
		return ErrInvalidWorkflowStep
	}
	if expectedVersion != current.Version {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.staged_discarded",
		events.String("workflow_id", string(current.ID)),
		events.Int("version", int64(current.PublishedVersion)),
	), now)
	if err != nil {
		return err
	}
	changed, err := m.Store.DiscardWorkflowStagedChanges(ctx, workspaceID, workflowID, expectedVersion, event)
	if err != nil {
		return err
	}
	if !changed {
		return store.ErrConflict
	}
	return nil
}

// DuplicateWorkflow copies a workflow's head into a new draft owned by the
// actor. Slack duplicates a published workflow into the builder as a new,
// unpublished workflow, so the copy starts as a draft at version 1 regardless
// of the source's status.
func (m Messages) DuplicateWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowDefinition, error) {
	current, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if current.OwnerID != actor {
		return domain.WorkflowDefinition{}, store.ErrNotFound
	}
	callbackID := current.CallbackID
	if callbackID != "" {
		callbackID += "-copy"
	}
	return m.CreateWorkflow(ctx, workspaceID, actor, domain.WorkflowDefinition{
		AppID:       current.AppID,
		Title:       current.Title + " (copy)",
		Description: current.Description,
		CallbackID:  callbackID,
		InputSchema: current.InputSchema,
		Steps:       current.Steps,
	})
}

// DeleteWorkflow removes a workflow and every record derived from it, like
// Slack's builder delete: revisions, triggers, featured entries, runs, and
// steps all go away in one transaction, and any execution still running is
// cancelled first so it ends in a terminal state rather than vanishing.
func (m Messages) DeleteWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID, expectedVersion uint64) error {
	current, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return err
	}
	if current.OwnerID != actor {
		return store.ErrNotFound
	}
	if expectedVersion != current.Version {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.deleted",
		events.String("workflow_id", string(current.ID)),
		events.String("app_id", string(current.AppID)),
	), now)
	if err != nil {
		return err
	}
	if _, err := m.Store.DeleteWorkflow(ctx, workspaceID, workflowID, expectedVersion, event); err != nil {
		return err
	}
	return nil
}

// WorkflowActivity returns the run dashboard a workflow owner sees in Slack's
// builder: how many runs are in each state and which runs happened most
// recently. Members do not read another owner's activity.
func (m Messages) WorkflowActivity(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowActivity, error) {
	current, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return domain.WorkflowActivity{}, err
	}
	if current.OwnerID != actor {
		return domain.WorkflowActivity{}, store.ErrNotFound
	}
	return m.Store.SummarizeWorkflowRuns(ctx, workspaceID, workflowID, workflowActivityRecentLimit)
}

const workflowActivityRecentLimit = 5

// WorkflowStepChanges reports the step-level differences between a published
// workflow's staged draft and its published revision. Only the owner reads the
// live head, so only the owner can observe the changes; a member receives
// ErrNotFound just as they do from the discard operation. A workflow with no
// staged edits has no changes to report.
func (m Messages) WorkflowStepChanges(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowStepChange, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, err
	}
	current, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return nil, err
	}
	if current.OwnerID != actor {
		return nil, store.ErrNotFound
	}
	if current.Status != domain.WorkflowPublished || current.Version == current.PublishedVersion {
		return nil, nil
	}
	revisions, err := m.Store.ListWorkflowRevisions(ctx, workspaceID, workflowID)
	if err != nil {
		return nil, err
	}
	var published domain.WorkflowRevision
	for _, revision := range revisions {
		if revision.Version == current.PublishedVersion {
			published = revision
			break
		}
	}
	if published.Version != current.PublishedVersion {
		return nil, nil
	}
	return diffWorkflowSteps(published.Steps, current.Steps), nil
}

// diffWorkflowSteps compares two step lists positionally. Each slot is judged
// against the published revision's step at the same index, so inserting a step
// above another marks every later position as changed until the next publish
// realigns them; the builder labels each numbered slot with exactly this. A
// slot counts as changed when any part of its step definition moved — the
// function, its title, or the routing metadata — not just the callback.
func diffWorkflowSteps(publishedRaw, headRaw string) []domain.WorkflowStepChange {
	published := decodeWorkflowStepFunctions(publishedRaw)
	head := decodeWorkflowStepFunctions(headRaw)
	var changes []domain.WorkflowStepChange
	for index := 0; index < max(len(published), len(head)); index++ {
		position := index + 1
		switch {
		case index >= len(head):
			changes = append(changes, domain.WorkflowStepChange{
				Position: position, FunctionID: published[index].FunctionID, Change: domain.WorkflowStepChangeRemoved,
			})
		case index >= len(published):
			changes = append(changes, domain.WorkflowStepChange{
				Position: position, FunctionID: head[index].FunctionID, Change: domain.WorkflowStepChangeAdded,
			})
		case canonicalWorkflowStep(head[index]) != canonicalWorkflowStep(published[index]):
			changes = append(changes, domain.WorkflowStepChange{
				Position: position, FunctionID: head[index].FunctionID, Change: domain.WorkflowStepChangeChanged,
			})
		}
	}
	return changes
}

// canonicalWorkflowStep renders one step in its normalized JSON form so two
// definitions compare by content, not by the caller's whitespace or field
// order. Revisions written before step types existed decode with an empty
// type, which the default repairs. Marshal errors are impossible for this
// struct shape, so a failure collapses to the zero string, which still
// differs from any valid encoding.
func canonicalWorkflowStep(step workflowFunctionDefinition) string {
	if step.Type == "" {
		step.Type = workflowStepTypeFunction
	}
	encoded, err := json.Marshal(step)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func decodeWorkflowStepFunctions(raw string) []workflowFunctionDefinition {
	var steps []workflowFunctionDefinition
	if json.Unmarshal([]byte(raw), &steps) != nil {
		return nil
	}
	return steps
}

func (m Messages) ListWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, request domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, false, "", err
	}
	if err := store.CheckAscendingPage(request); err != nil {
		return nil, false, "", err
	}
	// Visibility must precede pagination. Filtering one store page could return
	// an empty page with `more=true`, or under-fill every page when another
	// developer owns drafts between visible workflows. Scan bounded raw pages
	// until we have one visible look-ahead item or exhaust the workspace.
	visible := make([]domain.WorkflowDefinition, 0, request.Limit+1)
	cursor := request.Cursor
	batchSize := max(100, request.Limit+1)
	for len(visible) <= request.Limit {
		values, more, next, err := m.Store.ListWorkflows(ctx, workspaceID, domain.PageRequest{Limit: batchSize, Cursor: cursor})
		if err != nil {
			return nil, false, "", err
		}
		for _, value := range values {
			if value.OwnerID == actor || value.Status == domain.WorkflowPublished {
				visible = append(visible, value)
				if len(visible) > request.Limit {
					break
				}
			}
		}
		if len(visible) > request.Limit || !more {
			break
		}
		cursor = next
	}
	more := len(visible) > request.Limit
	if !more {
		return m.projectVisible(ctx, actor, visible), false, "", nil
	}
	visible = visible[:request.Limit]
	next, err := domain.NewListCursor(string(visible[len(visible)-1].ID))
	if err != nil {
		return nil, false, "", err
	}
	return m.projectVisible(ctx, actor, visible), true, next, nil
}

// projectVisible hides staged edits on workflows the caller does not own. The
// owner reads the live head; every other reader sees the published revision.
func (m Messages) projectVisible(ctx context.Context, actor domain.UserID, values []domain.WorkflowDefinition) []domain.WorkflowDefinition {
	projected := make([]domain.WorkflowDefinition, len(values))
	for index, value := range values {
		if value.OwnerID == actor {
			projected[index] = value
			continue
		}
		projection, err := m.publishedProjection(ctx, value)
		if err != nil {
			projected[index] = value
			continue
		}
		projected[index] = projection
	}
	return projected
}

func (m Messages) GetWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID) (domain.WorkflowDefinition, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.WorkflowDefinition{}, err
	}
	value, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		return domain.WorkflowDefinition{}, err
	}
	if value.OwnerID != actor && value.Status != domain.WorkflowPublished {
		return domain.WorkflowDefinition{}, store.ErrNotFound
	}
	if value.OwnerID == actor {
		return value, nil
	}
	return m.publishedProjection(ctx, value)
}

// publishedProjection hides staged edits from any caller that is not the owner.
// The head row carries the draft when a published workflow is being edited, so
// returning it verbatim would leak unpublished titles and steps to the
// directory and run views. Only the published revision is public; the owner and
// the execution path keep reading the live head.
func (m Messages) publishedProjection(ctx context.Context, value domain.WorkflowDefinition) (domain.WorkflowDefinition, error) {
	if value.PublishedVersion == 0 || value.Version == value.PublishedVersion {
		return value, nil
	}
	revisions, err := m.Store.ListWorkflowRevisions(ctx, value.WorkspaceID, value.ID)
	if err != nil {
		return value, nil
	}
	for _, revision := range revisions {
		if revision.Version != value.PublishedVersion {
			continue
		}
		value.Title = revision.Title
		value.Description = revision.Description
		value.CallbackID = revision.CallbackID
		value.InputSchema = revision.InputSchema
		value.Steps = revision.Steps
		return value, nil
	}
	return value, nil
}

func (m Messages) SetWorkflowTrigger(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, value domain.WorkflowTrigger, expectedVersion uint64) (domain.WorkflowTrigger, error) {
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, value.WorkflowID)
	if err != nil {
		return domain.WorkflowTrigger{}, err
	}
	if workflow.OwnerID != actor {
		return domain.WorkflowTrigger{}, store.ErrNotFound
	}
	value.Title = strings.TrimSpace(value.Title)
	value.Type = strings.TrimSpace(value.Type)
	switch domain.WorkflowTriggerType(value.Type) {
	case domain.WorkflowTriggerLink, domain.WorkflowTriggerShortcut, domain.WorkflowTriggerScheduled,
		domain.WorkflowTriggerWebhook, domain.WorkflowTriggerMessage, domain.WorkflowTriggerReaction,
		domain.WorkflowTriggerJoin, domain.WorkflowTriggerList:
	default:
		return domain.WorkflowTrigger{}, ErrInvalidTriggerConfig
	}
	if value.ID == "" {
		value.ID, err = domain.NewWorkflowTriggerID()
		if err != nil {
			return domain.WorkflowTrigger{}, err
		}
	}
	now := time.Now().UTC()
	value.WorkspaceID = workspaceID
	value.AppID = workflow.AppID
	value.UpdatedAt = now
	topic := "workflow.trigger_updated"
	var current *domain.WorkflowTrigger
	if expectedVersion == 0 {
		value.CreatedAt = now
		value.Version = 1
		topic = "workflow.trigger_created"
	} else {
		existing, getErr := m.Store.GetWorkflowTrigger(ctx, workspaceID, value.ID)
		if getErr != nil {
			return domain.WorkflowTrigger{}, getErr
		}
		if existing.WorkflowID != value.WorkflowID || existing.AppID != workflow.AppID {
			return domain.WorkflowTrigger{}, store.ErrNotFound
		}
		current = &existing
		value.CreatedAt = existing.CreatedAt
		value.Version = expectedVersion + 1
	}
	if err := m.normalizeWorkflowTriggerConfig(ctx, &value, current, now); err != nil {
		return domain.WorkflowTrigger{}, err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload(topic,
		events.String("workflow_id", string(value.WorkflowID)),
		events.String("trigger_id", string(value.ID)),
		events.String("app_id", string(value.AppID)),
	), now)
	if err != nil {
		return domain.WorkflowTrigger{}, err
	}
	if err := m.Store.SetWorkflowTrigger(ctx, value, expectedVersion, event); err != nil {
		return domain.WorkflowTrigger{}, err
	}
	return value, nil
}

func (m Messages) ListWorkflowTriggers(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, workflowID domain.WorkflowID) ([]domain.WorkflowTrigger, error) {
	workflow, err := m.GetWorkflow(ctx, workspaceID, actor, workflowID)
	if err != nil {
		return nil, err
	}
	values, err := m.Store.ListWorkflowTriggers(ctx, workspaceID, workflowID)
	if err != nil {
		return nil, err
	}
	if workflow.OwnerID == actor {
		return values, nil
	}
	visible := values[:0]
	for _, value := range values {
		if value.Enabled {
			visible = append(visible, value)
		}
	}
	return visible, nil
}

func functionInputs(step workflowFunctionDefinition, runInputs string) (string, error) {
	inputs := map[string]json.RawMessage{}
	if strings.TrimSpace(runInputs) != "" {
		if err := json.Unmarshal([]byte(runInputs), &inputs); err != nil {
			return "", ErrInvalidWorkflowStep
		}
	}
	if step.Inputs != nil {
		encoded, err := json.Marshal(step.Inputs)
		if err != nil {
			return "", err
		}
		var configured map[string]json.RawMessage
		if json.Unmarshal(encoded, &configured) != nil || configured == nil {
			return "", ErrInvalidWorkflowStep
		}
		for name, value := range configured {
			inputs[name] = value
		}
	}
	encoded, err := json.Marshal(inputs)
	return string(encoded), err
}

// resolveWorkflowStepInputs builds one execution's inputs: the run inputs
// merged with the step's static inputs, then the step's input mapping applied
// on top. A mapped variable keeps its JSON type; a variable that does not
// resolve removes the key entirely so a stale inherited value cannot leak
// through, and a mapping value without a variable prefix is a literal.
func resolveWorkflowStepInputs(step workflowFunctionDefinition, runInputs string, inputs map[string]any, outputs map[string]map[string]any) (string, error) {
	base, err := functionInputs(step, runInputs)
	if err != nil {
		return "", err
	}
	if len(step.InputMapping) == 0 {
		return base, nil
	}
	merged := map[string]json.RawMessage{}
	if json.Unmarshal([]byte(base), &merged) != nil || merged == nil {
		return "", ErrInvalidWorkflowStep
	}
	for name, target := range step.InputMapping {
		if source, err := parseWorkflowVariableSource(target); err == nil {
			value, found := resolveWorkflowVariable(source, inputs, outputs)
			if !found {
				delete(merged, name)
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			merged[name] = encoded
			continue
		}
		encoded, err := json.Marshal(target)
		if err != nil {
			return "", err
		}
		merged[name] = encoded
	}
	encoded, err := json.Marshal(merged)
	return string(encoded), err
}

func workflowFunctionID(appID domain.AppID, callbackID string) string {
	sum := sha256.Sum256([]byte(string(appID) + "\x00" + callbackID))
	return fmt.Sprintf("Fn%X", sum[:8])
}

func (m Messages) workflowFunctionSnapshot(ctx context.Context, appID domain.AppID, callbackID string) (slackFunctionSnapshot, error) {
	app, revision, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return slackFunctionSnapshot{}, err
	}
	parsed, problems := appmanifest.Parse(revision.Manifest)
	if len(problems) != 0 {
		return slackFunctionSnapshot{}, store.ErrConflict
	}
	function, exists := parsed.Functions[callbackID]
	if !exists {
		return slackFunctionSnapshot{}, store.ErrNotFound
	}
	return slackFunctionSnapshot{
		ID: workflowFunctionID(appID, callbackID), CallbackID: callbackID, Title: function.Title,
		Description: function.Description, Type: "app", InputParameters: function.InputParameters,
		OutputParameters: function.OutputParameters, AppID: appID, DateCreated: app.CreatedAt.Unix(),
		DateUpdated: app.UpdatedAt.Unix(), DateDeleted: 0, Runtime: parsed.FunctionRuntime,
	}, nil
}

func (m Messages) validateWorkflowFunctions(ctx context.Context, appID domain.AppID, steps []workflowFunctionDefinition) error {
	for _, step := range steps {
		if step.Type != workflowStepTypeFunction {
			continue
		}
		stepAppID := step.AppID
		if stepAppID == "" {
			stepAppID = appID
		}
		// Cross-app/built-in/connector steps need their own availability and
		// authorization model. Accepting them as ordinary callbacks would defer
		// a guaranteed failure until after a user publishes the workflow.
		if stepAppID != appID {
			return ErrInvalidWorkflowStep
		}
		function, err := m.workflowFunctionSnapshot(ctx, stepAppID, step.FunctionID)
		if err != nil || function.Runtime != "remote" {
			return ErrInvalidWorkflowStep
		}
	}
	return nil
}

// newWorkflowStepExecution creates the execution record for the next step a run
// reaches. A function step dispatches to its app and emits function_executed;
// a form or button step parks the run waiting for a person to submit or click,
// emitting workflow.step_waiting instead. The caller resolves stepOutputs once
// and passes it in so both input mapping and the advance reuse it.
func (m Messages) newWorkflowStepExecution(ctx context.Context, run domain.WorkflowRun, step workflowFunctionDefinition, defaultApp domain.AppID, actor domain.UserID, now time.Time, stepOutputs map[string]map[string]any) (domain.WorkflowStep, events.Event, error) {
	if step.Type == workflowStepTypeForm || step.Type == workflowStepTypeButton {
		executionID, err := domain.NewFunctionExecutionID()
		if err != nil {
			return domain.WorkflowStep{}, events.Event{}, err
		}
		stepName := step.Title
		if stepName == "" && step.Button != nil {
			stepName = step.Button.Label
		}
		execution := domain.WorkflowStep{
			ID: executionID, WorkflowRunID: run.ID, WorkspaceID: run.WorkspaceID, AppID: defaultApp, UserID: actor,
			EditID: step.ID, Status: domain.WorkflowStepWaiting, Inputs: "{}", Outputs: "{}",
			StepName: stepName, CreatedAt: now, UpdatedAt: now,
		}
		topic := "workflow.step_waiting"
		event, err := newEvent(run.WorkspaceID, actor, events.NewPayload(topic,
			events.String("workflow_run_id", string(run.ID)),
			events.String("step_id", step.ID),
			events.String("step_type", step.Type),
			events.String("function_execution_id", string(execution.ID)),
		), now)
		if err != nil {
			return domain.WorkflowStep{}, events.Event{}, err
		}
		return execution, event, nil
	}
	appID := step.AppID
	if appID == "" {
		appID = defaultApp
	}
	function, err := m.workflowFunctionSnapshot(ctx, appID, step.FunctionID)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	if function.Runtime != "remote" {
		return domain.WorkflowStep{}, events.Event{}, ErrInvalidWorkflowStep
	}
	executionID, err := domain.NewFunctionExecutionID()
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	inputs, err := resolveWorkflowStepInputs(step, run.Inputs, decodeWorkflowInputsMap(run.Inputs), stepOutputs)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	execution := domain.WorkflowStep{
		ID: executionID, WorkflowRunID: run.ID, WorkspaceID: run.WorkspaceID, AppID: appID, UserID: actor,
		FunctionID: function.ID, EditID: step.ID, Status: domain.WorkflowStepExecuting, Inputs: inputs, Outputs: "{}",
		StepName: step.Title, CreatedAt: now, UpdatedAt: now,
	}
	event, err := newEvent(run.WorkspaceID, actor, events.NewPayload("function_executed",
		events.String("target_app_id", string(appID)),
		events.String("function_execution_id", string(execution.ID)),
		events.String("function_id", function.ID),
		events.String("workflow_run_id", string(run.ID)),
	), now)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	encodedFunction, err := json.Marshal(function)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	snapshot, err := json.Marshal(functionExecutionSnapshot{
		AppID: appID, FunctionExecutionID: execution.ID, Function: encodedFunction,
		WorkflowRunID: run.ID, Inputs: json.RawMessage(inputs),
	})
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	event.PrivatePayload = string(snapshot)
	return execution, event, nil
}

func (m Messages) RunWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID domain.WorkflowTriggerID, conversationID domain.ConversationID, inputs, idempotencyKey string) (domain.WorkflowRun, error) {
	return m.runWorkflow(ctx, workspaceID, actor, triggerID, conversationID, inputs, idempotencyKey, false)
}

// RunAutomaticWorkflow starts a run from a system-driven trigger — a schedule,
// a webhook invocation, or a workspace event. Trigger permissions gate who may
// click a link or shortcut; they do not gate a fire the owner already
// configured, so automatic runs execute as the workflow owner and skip the
// permission and conversation checks. The published-workflow and
// enabled-trigger requirements still apply.
func (m Messages) RunAutomaticWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, triggerID domain.WorkflowTriggerID, conversationID domain.ConversationID, inputs, idempotencyKey string) (domain.WorkflowRun, error) {
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, trigger.WorkflowID)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	return m.runWorkflow(ctx, workspaceID, workflow.OwnerID, triggerID, conversationID, inputs, idempotencyKey, true)
}

func (m Messages) runWorkflow(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, triggerID domain.WorkflowTriggerID, conversationID domain.ConversationID, inputs, idempotencyKey string, automatic bool) (domain.WorkflowRun, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey != "" {
		existing, err := m.Store.GetWorkflowRunByIdempotency(ctx, workspaceID, idempotencyKey)
		if err == nil {
			if existing.ActorID != actor || existing.TriggerID != triggerID {
				return domain.WorkflowRun{}, store.ErrConflict
			}
			return existing, nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return domain.WorkflowRun{}, err
		}
	}
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, trigger.WorkflowID)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	if workflow.Status != domain.WorkflowPublished || !trigger.Enabled {
		return domain.WorkflowRun{}, store.ErrConflict
	}
	if !automatic {
		if conversationID != "" {
			if err := m.authorizeConversation(ctx, workspaceID, actor, conversationID); err != nil {
				return domain.WorkflowRun{}, err
			}
		} else if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
			return domain.WorkflowRun{}, err
		}
		if allowed, err := m.canRunWorkflowTrigger(ctx, workflow, trigger, actor, conversationID); err != nil {
			return domain.WorkflowRun{}, err
		} else if !allowed {
			return domain.WorkflowRun{}, ErrWorkflowPermissionDenied
		}
	}
	inputs, err = normalizeJSONObject(inputs, true)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	// Runs pin the published revision: the head may carry staged edits, so
	// the step list comes from the revision the run claims as its version.
	steps, err := m.workflowStepsAtVersion(ctx, workflow, workflow.PublishedVersion)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	runID, err := domain.NewWorkflowRunID()
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	now := time.Now().UTC()
	run := domain.WorkflowRun{
		ID: runID, WorkflowID: workflow.ID, WorkflowVersion: workflow.PublishedVersion, TriggerID: trigger.ID,
		WorkspaceID: workspaceID, AppID: workflow.AppID, ActorID: actor, ConversationID: conversationID,
		Status: domain.WorkflowRunRunning, Inputs: inputs, Outputs: "{}", CurrentStep: 0,
		IdempotencyKey: idempotencyKey, CreatedAt: now, UpdatedAt: now,
	}
	started, err := newEvent(workspaceID, actor, events.NewPayload("workflow.run_started",
		events.String("workflow_id", string(workflow.ID)),
		events.String("workflow_run_id", string(run.ID)),
		events.String("trigger_id", string(trigger.ID)),
	), now)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	emitted := []events.Event{started}
	var first *domain.WorkflowStep
	firstIndex := firstRunnableWorkflowStep(steps, 0, decodeWorkflowInputsMap(inputs), nil)
	if firstIndex < 0 {
		// Every step's condition failed (or there are no steps): the run
		// completes immediately without executing anything.
		run.Status = domain.WorkflowRunCompleted
		run.CompletedAt = now
	} else {
		run.CurrentStep = firstIndex
		execution, executionEvent, executionErr := m.newWorkflowStepExecution(ctx, run, steps[firstIndex], workflow.AppID, actor, now, nil)
		if executionErr != nil {
			return domain.WorkflowRun{}, executionErr
		}
		first = &execution
		emitted = append(emitted, executionEvent)
	}
	if err := m.Store.CreateWorkflowRun(ctx, run, first, emitted); err != nil {
		if idempotencyKey != "" && errors.Is(err, store.ErrAlreadyExists) {
			existing, lookupErr := m.Store.GetWorkflowRunByIdempotency(ctx, workspaceID, idempotencyKey)
			if lookupErr == nil && existing.ActorID == actor && existing.TriggerID == triggerID {
				return existing, nil
			}
		}
		return domain.WorkflowRun{}, err
	}
	return run, nil
}

func (m Messages) canRunWorkflowTrigger(ctx context.Context, workflow domain.WorkflowDefinition, trigger domain.WorkflowTrigger, actor domain.UserID, conversation domain.ConversationID) (bool, error) {
	permission, err := m.Store.GetAutomationPermission(ctx, workflow.WorkspaceID, "trigger", string(trigger.ID))
	if errors.Is(err, store.ErrNotFound) {
		return actor == workflow.OwnerID, nil
	}
	if err != nil {
		return false, err
	}
	switch permission.PermissionType {
	case "everyone":
		return true, nil
	case "app_collaborators":
		return actor == workflow.OwnerID, nil
	case "named_entities":
		for _, userID := range permission.UserIDs {
			if userID == actor {
				return true, nil
			}
		}
		for _, channelID := range permission.ChannelIDs {
			if channelID == conversation {
				return true, nil
			}
		}
		for _, teamID := range permission.TeamIDs {
			if teamID == workflow.WorkspaceID {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, ErrInvalidWorkflowStep
	}
}

func (m Messages) workflowStepsAtVersion(ctx context.Context, workflow domain.WorkflowDefinition, version uint64) ([]workflowFunctionDefinition, error) {
	if workflow.Version == version {
		_, steps, err := normalizeWorkflowSteps(workflow.Steps)
		return steps, err
	}
	revisions, err := m.Store.ListWorkflowRevisions(ctx, workflow.WorkspaceID, workflow.ID)
	if err != nil {
		return nil, err
	}
	for _, revision := range revisions {
		if revision.Version == version {
			_, steps, err := normalizeWorkflowSteps(revision.Steps)
			return steps, err
		}
	}
	return nil, store.ErrNotFound
}

func (m Messages) CompleteFunction(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, executionID domain.WorkflowStepID, outputs, failure string) error {
	execution, err := m.Store.GetWorkflowStep(ctx, workspaceID, executionID)
	if err != nil {
		return err
	}
	if execution.AppID == "" || execution.AppID != appID {
		return ErrFunctionAccessDenied
	}
	if execution.Status != domain.WorkflowStepExecuting {
		return ErrFunctionNotRunning
	}
	return m.advanceStep(ctx, workspaceID, actor, execution, outputs, failure)
}

// SubmitWorkflowForm completes a waiting form step with the submitted inputs.
// Any workspace member may submit, the same audience Slack grants a form link.
func (m Messages) SubmitWorkflowForm(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, runID domain.WorkflowRunID, stepID domain.WorkflowStepID, inputs string) error {
	execution, step, err := m.loadWaitingStep(ctx, workspaceID, actor, runID, stepID)
	if err != nil {
		return err
	}
	if step.Type != workflowStepTypeForm {
		return ErrFunctionNotRunning
	}
	return m.advanceStep(ctx, workspaceID, actor, execution, inputs, "")
}

// CompleteWorkflowButton completes a waiting button step. Any workspace member
// may click, matching Slack's approval step.
func (m Messages) CompleteWorkflowButton(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, runID domain.WorkflowRunID, stepID domain.WorkflowStepID) error {
	execution, step, err := m.loadWaitingStep(ctx, workspaceID, actor, runID, stepID)
	if err != nil {
		return err
	}
	if step.Type != workflowStepTypeButton {
		return ErrFunctionNotRunning
	}
	return m.advanceStep(ctx, workspaceID, actor, execution, "{}", "")
}

// loadWaitingStep fetches a run's current execution, checks it is still waiting
// for human input, and resolves the workflow step definition it corresponds to.
func (m Messages) loadWaitingStep(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, runID domain.WorkflowRunID, stepID domain.WorkflowStepID) (domain.WorkflowStep, workflowFunctionDefinition, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, err
	}
	execution, err := m.Store.GetWorkflowStep(ctx, workspaceID, stepID)
	if err != nil {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, err
	}
	if execution.WorkflowRunID != runID || execution.Status != domain.WorkflowStepWaiting {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, ErrFunctionNotRunning
	}
	run, err := m.Store.GetWorkflowRun(ctx, workspaceID, runID)
	if err != nil {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, err
	}
	if run.Status != domain.WorkflowRunRunning {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, ErrFunctionNotRunning
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, run.WorkflowID)
	if err != nil {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, err
	}
	steps, err := m.workflowStepsAtVersion(ctx, workflow, run.WorkflowVersion)
	if err != nil {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, err
	}
	if run.CurrentStep < 0 || run.CurrentStep >= len(steps) || steps[run.CurrentStep].ID != execution.EditID {
		return domain.WorkflowStep{}, workflowFunctionDefinition{}, ErrFunctionNotRunning
	}
	return execution, steps[run.CurrentStep], nil
}

// WorkflowRunInteraction reports the human input a running workflow is waiting
// on — a form to fill or a button to click — so a run view can render it. Any
// workspace member may read the interaction, matching the audience that may
// submit it.
func (m Messages) WorkflowRunInteraction(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, runID domain.WorkflowRunID) (domain.WorkflowInteraction, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.WorkflowInteraction{}, err
	}
	run, err := m.Store.GetWorkflowRun(ctx, workspaceID, runID)
	if err != nil {
		return domain.WorkflowInteraction{}, err
	}
	if run.Status != domain.WorkflowRunRunning {
		return domain.WorkflowInteraction{}, nil
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, run.WorkflowID)
	if err != nil {
		return domain.WorkflowInteraction{}, err
	}
	steps, err := m.workflowStepsAtVersion(ctx, workflow, run.WorkflowVersion)
	if err != nil {
		return domain.WorkflowInteraction{}, err
	}
	if run.CurrentStep < 0 || run.CurrentStep >= len(steps) {
		return domain.WorkflowInteraction{}, nil
	}
	step := steps[run.CurrentStep]
	if step.Type != workflowStepTypeForm && step.Type != workflowStepTypeButton {
		return domain.WorkflowInteraction{}, nil
	}
	executions, err := m.Store.ListWorkflowRunSteps(ctx, workspaceID, runID)
	if err != nil {
		return domain.WorkflowInteraction{}, err
	}
	for _, execution := range executions {
		if execution.Status == domain.WorkflowStepWaiting && execution.EditID == step.ID {
			interaction := domain.WorkflowInteraction{StepID: execution.ID, Kind: step.Type}
			if step.Form != nil {
				interaction.Title = step.Form.Title
				for name, label := range step.Form.Inputs {
					interaction.Fields = append(interaction.Fields, domain.WorkflowInteractionField{Name: name, Label: label})
				}
				slices.SortFunc(interaction.Fields, func(a, b domain.WorkflowInteractionField) int {
					return strings.Compare(a.Name, b.Name)
				})
			}
			if step.Button != nil {
				interaction.Label = step.Button.Label
			}
			return interaction, nil
		}
	}
	return domain.WorkflowInteraction{}, nil
}

// advanceStep marks one step complete (or failed) and advances the run to the
// next step whose condition holds. It is the shared tail of function
// completion, form submission, and button clicks.
func (m Messages) advanceStep(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, execution domain.WorkflowStep, outputs, failure string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return err
	}
	run, err := m.Store.GetWorkflowRun(ctx, workspaceID, execution.WorkflowRunID)
	if err != nil {
		return err
	}
	workflow, err := m.Store.GetWorkflow(ctx, workspaceID, run.WorkflowID)
	if err != nil {
		return err
	}
	steps, err := m.workflowStepsAtVersion(ctx, workflow, run.WorkflowVersion)
	if err != nil {
		return err
	}
	if run.CurrentStep < 0 || run.CurrentStep >= len(steps) {
		return store.ErrConflict
	}
	// Executions created before unique step ids carried the callback as their
	// EditID; accept either the step's id or its callback so an in-flight run
	// from either generation can complete.
	if steps[run.CurrentStep].ID != execution.EditID && steps[run.CurrentStep].FunctionID != execution.EditID {
		return store.ErrConflict
	}
	// Branch conditions on later steps resolve against every completed
	// step's outputs, including the one this call just finished.
	executions, err := m.Store.ListWorkflowRunSteps(ctx, workspaceID, run.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	execution.UpdatedAt = now
	expectedStep := run.CurrentStep
	var next *domain.WorkflowStep
	emitted := make([]events.Event, 0, 2)
	if strings.TrimSpace(failure) != "" {
		execution.Status = domain.WorkflowStepFailed
		execution.Error = strings.TrimSpace(failure)
		run.Status = domain.WorkflowRunFailed
		run.Error = execution.Error
		run.CompletedAt = now
	} else {
		outputs, err = normalizeJSONObject(outputs, true)
		if err != nil {
			return err
		}
		execution.Status = domain.WorkflowStepCompleted
		execution.Outputs = outputs
		run.Outputs = outputs
		stepOutputs := workflowStepOutputsByID(append(executions, execution))
		nextIndex := firstRunnableWorkflowStep(steps, run.CurrentStep+1, decodeWorkflowInputsMap(run.Inputs), stepOutputs)
		if nextIndex < 0 {
			run.Status = domain.WorkflowRunCompleted
			run.CompletedAt = now
		} else {
			nextValue, nextEvent, nextErr := m.newWorkflowStepExecution(ctx, run, steps[nextIndex], workflow.AppID, run.ActorID, now, stepOutputs)
			if nextErr != nil {
				return nextErr
			}
			next = &nextValue
			emitted = append(emitted, nextEvent)
			run.CurrentStep = nextIndex
		}
	}
	run.UpdatedAt = now
	topic := "workflow.run_completed"
	if run.Status == domain.WorkflowRunFailed {
		topic = "workflow.run_failed"
	} else if run.Status == domain.WorkflowRunRunning {
		topic = "workflow.run_advanced"
	}
	runEvent, err := newEvent(workspaceID, actor, events.NewPayload(topic,
		events.String("workflow_id", string(workflow.ID)),
		events.String("workflow_run_id", string(run.ID)),
		events.String("function_execution_id", string(execution.ID)),
	), now)
	if err != nil {
		return err
	}
	emitted = append([]events.Event{runEvent}, emitted...)
	return m.Store.AdvanceWorkflowRun(ctx, execution, next, run, expectedStep, emitted)
}

func (m Messages) GetWorkflowRun(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, runID domain.WorkflowRunID) (domain.WorkflowRun, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.WorkflowRun{}, err
	}
	run, err := m.Store.GetWorkflowRun(ctx, workspaceID, runID)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	if run.ActorID != actor {
		workflow, workflowErr := m.Store.GetWorkflow(ctx, workspaceID, run.WorkflowID)
		if workflowErr != nil {
			return domain.WorkflowRun{}, workflowErr
		}
		if workflow.OwnerID != actor {
			return domain.WorkflowRun{}, store.ErrNotFound
		}
	}
	return run, nil
}

func (m Messages) resolveWorkflowFunction(ctx context.Context, appID domain.AppID, functionID, callbackID string) (slackFunctionSnapshot, error) {
	functionID = strings.TrimSpace(functionID)
	callbackID = strings.TrimSpace(callbackID)
	if callbackID != "" {
		function, err := m.workflowFunctionSnapshot(ctx, appID, callbackID)
		if err != nil {
			return slackFunctionSnapshot{}, err
		}
		if functionID != "" && function.ID != functionID {
			return slackFunctionSnapshot{}, store.ErrNotFound
		}
		return function, nil
	}
	app, revision, err := m.Store.GetApp(ctx, appID)
	if err != nil {
		return slackFunctionSnapshot{}, err
	}
	parsed, problems := appmanifest.Parse(revision.Manifest)
	if len(problems) != 0 {
		return slackFunctionSnapshot{}, store.ErrConflict
	}
	for candidate := range parsed.Functions {
		if workflowFunctionID(app.ID, candidate) == functionID {
			return m.workflowFunctionSnapshot(ctx, appID, candidate)
		}
	}
	return slackFunctionSnapshot{}, store.ErrNotFound
}

func (m Messages) GetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, functionID, callbackID string) (domain.AutomationPermission, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.AutomationPermission{}, err
	}
	function, err := m.resolveWorkflowFunction(ctx, appID, functionID, callbackID)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	value, err := m.Store.GetAutomationPermission(ctx, workspaceID, "function", function.ID)
	if errors.Is(err, store.ErrNotFound) {
		value = domain.AutomationPermission{
			ResourceType: "function", ResourceID: function.ID, WorkspaceID: workspaceID, AppID: appID,
			PermissionType: "app_collaborators",
		}
		return m.withAppCollaboratorOwner(ctx, value)
	}
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if value.AppID != appID {
		return domain.AutomationPermission{}, ErrFunctionAccessDenied
	}
	return m.withAppCollaboratorOwner(ctx, value)
}

func (m Messages) SetFunctionPermission(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, functionID, callbackID string, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	function, err := m.resolveWorkflowFunction(ctx, appID, functionID, callbackID)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.AutomationPermission{}, err
	}
	if !slices.Contains([]string{"everyone", "app_collaborators", "named_entities", "system"}, value.PermissionType) {
		return domain.AutomationPermission{}, ErrInvalidWorkflowStep
	}
	value.ResourceType = "function"
	value.ResourceID = function.ID
	value.WorkspaceID = workspaceID
	value.AppID = appID
	value.UpdatedAt = time.Now().UTC()
	if err := m.validateAutomationEntities(ctx, &value); err != nil {
		return domain.AutomationPermission{}, err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("function.permission_set",
		events.String("target_app_id", string(appID)),
		events.String("function_id", function.ID),
		events.String("permission_type", value.PermissionType),
	), value.UpdatedAt)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if err := m.Store.SetAutomationPermission(ctx, value, event); err != nil {
		return domain.AutomationPermission{}, err
	}
	return m.withAppCollaboratorOwner(ctx, value)
}

func (m Messages) GetTriggerPermission(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, triggerID domain.WorkflowTriggerID) (domain.AutomationPermission, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.AutomationPermission{}, err
	}
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if trigger.AppID != appID {
		return domain.AutomationPermission{}, ErrFunctionAccessDenied
	}
	value, err := m.Store.GetAutomationPermission(ctx, workspaceID, "trigger", string(triggerID))
	if errors.Is(err, store.ErrNotFound) {
		value = domain.AutomationPermission{
			ResourceType: "trigger", ResourceID: string(triggerID), WorkspaceID: workspaceID, AppID: appID,
			PermissionType: "app_collaborators",
		}
		return m.withAppCollaboratorOwner(ctx, value)
	}
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if value.AppID != appID {
		return domain.AutomationPermission{}, ErrFunctionAccessDenied
	}
	return m.withAppCollaboratorOwner(ctx, value)
}

func (m Messages) SetTriggerPermission(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, triggerID domain.WorkflowTriggerID, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if trigger.AppID != appID {
		return domain.AutomationPermission{}, ErrFunctionAccessDenied
	}
	if !slices.Contains([]string{"everyone", "app_collaborators", "named_entities"}, value.PermissionType) {
		return domain.AutomationPermission{}, ErrInvalidWorkflowStep
	}
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return domain.AutomationPermission{}, err
	}
	value.ResourceType = "trigger"
	value.ResourceID = string(triggerID)
	value.WorkspaceID = workspaceID
	value.AppID = appID
	value.UpdatedAt = time.Now().UTC()
	if err := m.validateAutomationEntities(ctx, &value); err != nil {
		return domain.AutomationPermission{}, err
	}
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.trigger_permission_set",
		events.String("target_app_id", string(appID)),
		events.String("trigger_id", string(triggerID)),
		events.String("permission_type", value.PermissionType),
	), value.UpdatedAt)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if err := m.Store.SetAutomationPermission(ctx, value, event); err != nil {
		return domain.AutomationPermission{}, err
	}
	return m.withAppCollaboratorOwner(ctx, value)
}

// withAppCollaboratorOwner projects the collaborator identity Slack includes
// in permission responses without persisting it as named-entity state. The
// current developer-app model has one collaborator (the owner); when that
// model grows, this one projection point can return the full collaborator set.
func (m Messages) withAppCollaboratorOwner(ctx context.Context, value domain.AutomationPermission) (domain.AutomationPermission, error) {
	if value.PermissionType != "app_collaborators" {
		return value, nil
	}
	app, _, err := m.Store.GetApp(ctx, value.AppID)
	if err != nil {
		return domain.AutomationPermission{}, err
	}
	if app.DevelopmentWorkspaceID != value.WorkspaceID {
		return domain.AutomationPermission{}, store.ErrNotFound
	}
	value.UserIDs = []domain.UserID{app.OwnerID}
	return value, nil
}

func (m Messages) validateAutomationEntities(ctx context.Context, value *domain.AutomationPermission) error {
	if value.PermissionType != "named_entities" {
		value.UserIDs = nil
		value.ChannelIDs = nil
		value.TeamIDs = nil
		value.OrgIDs = nil
		return nil
	}
	if len(value.UserIDs)+len(value.ChannelIDs)+len(value.TeamIDs)+len(value.OrgIDs) == 0 {
		return ErrAutomationEntitiesEmpty
	}
	for _, userID := range value.UserIDs {
		user, err := m.Store.GetUser(ctx, userID)
		if err != nil || user.WorkspaceID != value.WorkspaceID {
			return ErrAutomationUserNotFound
		}
	}
	for _, channelID := range value.ChannelIDs {
		channel, err := m.Store.GetConversation(ctx, channelID)
		if err != nil || channel.WorkspaceID != value.WorkspaceID {
			return ErrAutomationChannelNotFound
		}
	}
	for _, teamID := range value.TeamIDs {
		if teamID != value.WorkspaceID {
			return ErrAutomationTeamNotFound
		}
	}
	for _, orgID := range value.OrgIDs {
		if strings.TrimSpace(orgID) == "" {
			return ErrAutomationOrgNotFound
		}
	}
	return nil
}

func (m Messages) SetFeaturedWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationID domain.ConversationID, triggerIDs []domain.WorkflowTriggerID) error {
	if len(triggerIDs) > 15 {
		return ErrInvalidWorkflowStep
	}
	if err := m.authorizeConversation(ctx, workspaceID, actor, conversationID); err != nil {
		return err
	}
	values := make([]domain.FeaturedWorkflow, len(triggerIDs))
	for index, triggerID := range triggerIDs {
		trigger, err := m.Store.GetWorkflowTrigger(ctx, workspaceID, triggerID)
		if err != nil {
			return err
		}
		if trigger.Type != "link" {
			return ErrInvalidWorkflowStep
		}
		workflow, err := m.Store.GetWorkflow(ctx, workspaceID, trigger.WorkflowID)
		if err != nil {
			return err
		}
		allowed, err := m.canRunWorkflowTrigger(ctx, workflow, trigger, actor, conversationID)
		if err != nil {
			return err
		}
		if !allowed || workflow.OwnerID != actor {
			return ErrWorkflowPermissionDenied
		}
		values[index] = domain.FeaturedWorkflow{
			WorkspaceID: workspaceID, ConversationID: conversationID, TriggerID: triggerID,
			Title: trigger.Title, Position: index,
		}
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, actor, events.NewPayload("workflow.featured_set",
		events.String("channel_id", string(conversationID)),
		events.Int("trigger_count", int64(len(values))),
	), now)
	if err != nil {
		return err
	}
	return m.Store.SetFeaturedWorkflows(ctx, workspaceID, conversationID, values, event)
}

func (m Messages) ListFeaturedWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, conversationIDs []domain.ConversationID) ([]domain.FeaturedWorkflow, error) {
	if len(conversationIDs) == 0 {
		return nil, ErrInvalidWorkflowStep
	}
	for _, conversationID := range conversationIDs {
		if err := m.authorizeConversation(ctx, workspaceID, actor, conversationID); err != nil {
			return nil, err
		}
	}
	return m.Store.ListFeaturedWorkflows(ctx, workspaceID, conversationIDs)
}

func (m Messages) ListFunctionWorkflowSteps(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, appID domain.AppID, functionID string, workflowID domain.WorkflowID, workflowReference string, workflowAppID domain.AppID) ([]domain.WorkflowStepVersion, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, err
	}
	if strings.TrimSpace(functionID) == "" {
		return nil, ErrInvalidWorkflowStep
	}
	function, err := m.resolveWorkflowFunction(ctx, appID, functionID, "")
	if err != nil || function.ID != functionID {
		return nil, ErrWorkflowFunctionNotFound
	}
	var workflows []domain.WorkflowDefinition
	if workflowID != "" {
		workflow, err := m.Store.GetWorkflow(ctx, workspaceID, workflowID)
		if err != nil {
			return nil, err
		}
		workflows = []domain.WorkflowDefinition{workflow}
	} else {
		callbackID := strings.TrimPrefix(strings.TrimSpace(workflowReference), "#/workflows/")
		if callbackID == "" || workflowAppID == "" {
			return nil, ErrInvalidWorkflowStep
		}
		var cursor domain.Cursor
		for {
			page, more, next, err := m.Store.ListWorkflows(ctx, workspaceID, domain.PageRequest{Limit: 100, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			for _, workflow := range page {
				if workflow.AppID == workflowAppID && workflow.CallbackID == callbackID {
					workflows = append(workflows, workflow)
				}
			}
			if !more {
				break
			}
			cursor = next
		}
		if len(workflows) == 0 {
			return nil, store.ErrNotFound
		}
	}
	values := make([]domain.WorkflowStepVersion, 0)
	for _, workflow := range workflows {
		revisions, err := m.Store.ListWorkflowRevisions(ctx, workspaceID, workflow.ID)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			_, steps, err := normalizeWorkflowSteps(revision.Steps)
			if err != nil {
				return nil, err
			}
			for index, step := range steps {
				stepAppID := step.AppID
				if stepAppID == "" {
					stepAppID = workflow.AppID
				}
				if workflowFunctionID(stepAppID, step.FunctionID) != functionID {
					continue
				}
				title := step.Title
				if title == "" {
					if function, err := m.workflowFunctionSnapshot(ctx, stepAppID, step.FunctionID); err == nil {
						title = function.Title
					}
				}
				values = append(values, domain.WorkflowStepVersion{
					Title: title, WorkflowID: workflow.ID, StepID: strconv.Itoa(index), IsDeleted: false,
					WorkflowVersionCreated: strconv.FormatInt(revision.CreatedAt.UnixMicro(), 10),
				})
			}
		}
	}
	return values, nil
}
