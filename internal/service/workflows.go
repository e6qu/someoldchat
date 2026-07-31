package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/appmanifest"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type workflowFunctionDefinition struct {
	ID         string       `json:"id,omitempty"`
	FunctionID string       `json:"function_id"`
	AppID      domain.AppID `json:"app_id,omitempty"`
	Title      string       `json:"title,omitempty"`
	Inputs     any          `json:"inputs,omitempty"`
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
	for index := range values {
		values[index].ID = strings.TrimSpace(values[index].ID)
		values[index].FunctionID = strings.TrimSpace(values[index].FunctionID)
		values[index].Title = strings.TrimSpace(values[index].Title)
		if values[index].FunctionID == "" {
			return "", nil, ErrInvalidWorkflowStep
		}
		if values[index].ID == "" {
			values[index].ID = values[index].FunctionID
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), values, nil
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
	steps, _, err := normalizeWorkflowSteps(value.Steps)
	if err != nil {
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
	steps, _, err := normalizeWorkflowSteps(value.Steps)
	if err != nil {
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
	value.Status = domain.WorkflowDraft
	topic := "workflow.updated"
	if publish {
		value.Status = domain.WorkflowPublished
		value.PublishedVersion = expectedVersion + 1
		topic = "workflow.published"
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

func (m Messages) ListWorkflows(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, request domain.PageRequest) ([]domain.WorkflowDefinition, bool, domain.Cursor, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, actor); err != nil {
		return nil, false, "", err
	}
	return m.Store.ListWorkflows(ctx, workspaceID, request)
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
	if value.Type != "link" && value.Type != "shortcut" && value.Type != "webhook" && value.Type != "scheduled" {
		return domain.WorkflowTrigger{}, ErrInvalidWorkflowStep
	}
	config, err := normalizeJSONObject(value.Config, true)
	if err != nil {
		return domain.WorkflowTrigger{}, err
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
	value.Config = config
	value.UpdatedAt = now
	topic := "workflow.trigger_updated"
	if expectedVersion == 0 {
		value.CreatedAt = now
		value.Version = 1
		topic = "workflow.trigger_created"
	} else {
		current, getErr := m.Store.GetWorkflowTrigger(ctx, workspaceID, value.ID)
		if getErr != nil {
			return domain.WorkflowTrigger{}, getErr
		}
		value.CreatedAt = current.CreatedAt
		value.Version = expectedVersion + 1
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
		DateUpdated: app.UpdatedAt.Unix(), DateDeleted: 0,
	}, nil
}

func (m Messages) newFunctionExecution(ctx context.Context, run domain.WorkflowRun, step workflowFunctionDefinition, defaultApp domain.AppID, actor domain.UserID, now time.Time) (domain.WorkflowStep, events.Event, error) {
	appID := step.AppID
	if appID == "" {
		appID = defaultApp
	}
	function, err := m.workflowFunctionSnapshot(ctx, appID, step.FunctionID)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	executionID, err := domain.NewFunctionExecutionID()
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	inputs, err := functionInputs(step, run.Inputs)
	if err != nil {
		return domain.WorkflowStep{}, events.Event{}, err
	}
	execution := domain.WorkflowStep{
		ID: executionID, WorkflowRunID: run.ID, WorkspaceID: run.WorkspaceID, AppID: appID, UserID: actor,
		FunctionID: function.ID, EditID: function.CallbackID, Status: domain.WorkflowStepExecuting, Inputs: inputs, Outputs: "{}",
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
	inputs, err = normalizeJSONObject(inputs, true)
	if err != nil {
		return domain.WorkflowRun{}, err
	}
	_, steps, err := normalizeWorkflowSteps(workflow.Steps)
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
	if len(steps) == 0 {
		run.Status = domain.WorkflowRunCompleted
		run.CompletedAt = now
	} else {
		execution, executionEvent, executionErr := m.newFunctionExecution(ctx, run, steps[0], workflow.AppID, actor, now)
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
	_, steps, err := normalizeWorkflowSteps(workflow.Steps)
	if err != nil {
		return err
	}
	if run.CurrentStep < 0 || run.CurrentStep >= len(steps) || steps[run.CurrentStep].FunctionID != execution.EditID {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	execution.UpdatedAt = now
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
		if run.CurrentStep+1 >= len(steps) {
			run.Status = domain.WorkflowRunCompleted
			run.CompletedAt = now
		} else {
			nextValue, nextEvent, nextErr := m.newFunctionExecution(ctx, run, steps[run.CurrentStep+1], workflow.AppID, run.ActorID, now)
			if nextErr != nil {
				return nextErr
			}
			next = &nextValue
			emitted = append(emitted, nextEvent)
		}
	}
	expectedStep := run.CurrentStep
	if next != nil {
		run.CurrentStep++
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
