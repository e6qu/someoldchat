package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/secretbox"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// Function-scoped interactivity.
//
// Slack hands a function's app an execution-scoped bot token (xwfp-) as
// bot_access_token with function_executed. What the app posts or opens with
// that token belongs to the execution, and every block_actions,
// view_submission and view_closed that comes from it carries the execution's
// function_data together with the same bot_access_token, so an SDK can answer
// the interaction with functions.completeSuccess/completeError (Bolt's
// complete() and fail()). The token acts as the app's bot and stops
// authenticating when the execution ends (store.LookupToken).

// FunctionExecutionTokenStore is what minting an execution token needs.
type FunctionExecutionTokenStore interface {
	IssueFunctionExecutionToken(context.Context, domain.FunctionExecutionToken, string) (domain.FunctionExecutionToken, error)
}

// functionExecutionTokenAssociatedData binds a sealed execution token to the
// one execution it was issued for.
func functionExecutionTokenAssociatedData(workspaceID domain.WorkspaceID, executionID domain.WorkflowStepID) string {
	return "function_execution_token:" + string(workspaceID) + ":" + string(executionID)
}

// issueFunctionExecutionToken returns the execution's token, minting it the
// first time function_executed is delivered. The bot authorization supplies
// the identity and scopes it acts with. A redelivery, or a concurrent
// delivery to a second connection, reads back the token already recorded, so
// the app is never handed two tokens for one execution.
func issueFunctionExecutionToken(ctx context.Context, state FunctionExecutionTokenStore, credentialKey []byte, bot domain.AppAuthorization, executionID domain.WorkflowStepID, callbackID string, now time.Time) (string, error) {
	plaintext, err := domain.NewFunctionExecutionToken()
	if err != nil {
		return "", err
	}
	associated := functionExecutionTokenAssociatedData(bot.WorkspaceID, executionID)
	ciphertext, err := secretbox.Seal(credentialKey, associated, plaintext)
	if err != nil {
		return "", err
	}
	stored, err := state.IssueFunctionExecutionToken(ctx, domain.FunctionExecutionToken{
		WorkspaceID: bot.WorkspaceID, ExecutionID: executionID, AppID: bot.AppID, CallbackID: callbackID,
		UserID: bot.UserID, BotID: bot.BotID, Scopes: bot.Scopes, Ciphertext: ciphertext, CreatedAt: now,
	}, domain.HashToken(plaintext))
	if err != nil {
		return "", err
	}
	return secretbox.Open(credentialKey, associated, stored.Ciphertext)
}

// functionCallbackID reads callback_id from a function_executed function
// snapshot.
func functionCallbackID(function json.RawMessage) string {
	var value struct {
		CallbackID string `json:"callback_id"`
	}
	_ = json.Unmarshal(function, &value)
	return value.CallbackID
}

// withFunctionInteraction adds function_data, bot_access_token and
// interactivity to an interaction payload whose message or view belongs to a
// function execution that is still running, exactly as Slack scopes such an
// interaction to the function. A source outside any execution, or one whose
// execution already ended, is delivered as an ordinary interaction: its
// execution token no longer authenticates, so handing it over would only
// break the SDK client that switches to it.
func (m Messages) withFunctionInteraction(ctx context.Context, payload map[string]any, workspaceID domain.WorkspaceID, appID domain.AppID, execution domain.WorkflowStepID, userID domain.UserID, triggerID string) error {
	if execution == "" {
		return nil
	}
	step, err := m.Store.GetWorkflowStep(ctx, workspaceID, execution)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if step.AppID != appID || step.Status != domain.WorkflowStepExecuting {
		return nil
	}
	token, err := m.Store.GetFunctionExecutionToken(ctx, workspaceID, execution)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	plaintext, err := secretbox.Open(m.AppCredentialKey, functionExecutionTokenAssociatedData(workspaceID, execution), token.Ciphertext)
	if err != nil {
		return err
	}
	inputs := json.RawMessage(step.Inputs)
	if strings.TrimSpace(step.Inputs) == "" || !json.Valid(inputs) {
		inputs = json.RawMessage(`{}`)
	}
	payload["function_data"] = map[string]any{
		"execution_id": execution,
		"function":     map[string]any{"callback_id": token.CallbackID},
		"inputs":       inputs,
	}
	payload["bot_access_token"] = plaintext
	if triggerID != "" {
		// The interactivity pointer names the interaction's own trigger:
		// views.open and views.push accept it in place of trigger_id.
		secret, err := domain.PublicID("")
		if err != nil {
			return err
		}
		payload["interactivity"] = map[string]any{
			"interactor":            map[string]any{"id": userID, "secret": secret},
			"interactivity_pointer": triggerID,
		}
	}
	return nil
}
