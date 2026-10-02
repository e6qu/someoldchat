package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"

	"github.com/sameoldchat/sameoldchat/internal/store"
)

// newDialogView renders a legacy dialog with the modal machinery. Each
// element becomes one input whose block and action are the element's name,
// so failures keyed by name (the app's {"errors":[{"name","error"}]}) land
// on the right field.
func (h Handler) newDialogView(ctx context.Context, principal auth.Principal, value domain.Dialog, failures map[string]string, submitted modalFormState) (*modalView, error) {
	definition, err := domain.ParseDialog(value.Payload)
	if err != nil {
		return nil, err
	}
	result := &modalView{
		ID: string(value.ID), AppID: string(value.AppID), Title: definition.Title, Close: "Cancel",
		Submit: strings.TrimSpace(definition.SubmitLabel), CallbackID: definition.CallbackID,
		Error: strings.TrimSpace(failures[""]), Dialog: true,
	}
	if result.Submit == "" {
		result.Submit = "Submit"
	}
	catalog := appActionOptionCatalog{}
	for index, element := range definition.Elements {
		input := modalInputView{
			Index: index, BlockID: element.Name, ActionID: element.Name, Label: element.Label,
			Hint: element.Hint, Placeholder: element.Placeholder, Optional: element.Optional, Value: element.Value,
		}
		switch element.Type {
		case "text", "textarea":
			input.Type, input.Control = "plain_text_input", "text"
			switch {
			case element.Type == "textarea":
				input.Control = "textarea"
			case element.Subtype == "email" || element.Subtype == "number" || element.Subtype == "url":
				input.Control = element.Subtype
			}
			input.MinLength, input.MaxLength = element.Limits()
		case "select":
			input.Type, input.Control = "static_select", "select"
			initial := element.Value
			if initial == "" && len(element.SelectedOptions) != 0 {
				initial = element.SelectedOptions[0].Value
			}
			switch element.DataSource {
			case "users", "channels", "conversations":
				action := messageActionView{Type: element.DataSource + "_select", Control: "select", InitialValues: []string{initial}}
				holder := []messageBlockView{{Actions: []messageActionView{action}}}
				catalog.enrich(ctx, h, principal, holder)
				input.Options = holder[0].Actions[0].Options
			case "external":
				// The options come from the app through dialog_suggestion,
				// loaded by the same control a Block Kit external select
				// uses; the dialog's selected_options is its initial choice.
				input.Type, input.Control = "external_select", "external"
				input.MinQueryLength = 1
				if element.MinQueryLength != nil && *element.MinQueryLength >= 0 {
					input.MinQueryLength = *element.MinQueryLength
				}
				for _, option := range element.SelectedOptions {
					input.Options = append(input.Options, messageActionOptionView{Text: option.Label, Value: option.Value, Selected: option.Value == initial})
				}
			default:
				for _, option := range element.Options {
					input.Options = append(input.Options, messageActionOptionView{Text: option.Label, Value: option.Value, Selected: option.Value == initial})
				}
				for _, group := range element.OptionGroups {
					for _, option := range group.Options {
						input.Options = append(input.Options, messageActionOptionView{Text: group.Label + " — " + option.Label, Value: option.Value, Selected: option.Value == initial})
					}
				}
			}
			input.Value = initial
		}
		if values, ok := submitted[index]; ok {
			input.Value = firstValue(values)
			input.Values = append([]string(nil), values...)
			if input.Control == "external" {
				input.Options = withChosenOptions(input.Options, values)
			}
			markSelectedOptions(input.Options, values)
		}
		result.Blocks = append(result.Blocks, modalBlockView{
			ID: element.Name, Kind: "input", Input: &input, Error: strings.TrimSpace(failures[element.Name]),
		})
	}
	return result, nil
}

func (h Handler) dialogSubmit(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	values, ok := h.decodeModalMutation(w, r, "dialog_id")
	if !ok {
		return
	}
	dialogID := domain.DialogID(strings.TrimSpace(values["dialog_id"][0]))
	current, err := h.Messages.CurrentDialog(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil || current.ID != dialogID {
		h.writeMutationError(w, r, http.StatusNotFound, "That app dialog is no longer open", "Reload the workspace to see the app's current dialog.")
		return
	}
	rendered, err := h.newDialogView(r.Context(), principal, current, nil, nil)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadGateway, "That app dialog is invalid", "The app supplied a dialog that SameOldChat could not render safely.")
		return
	}
	submission := make(map[string]string, len(rendered.Blocks))
	submitted := make(modalFormState, len(rendered.Blocks))
	for _, block := range rendered.Blocks {
		entered := append([]string(nil), values[fmt.Sprintf("input_%d", block.Input.Index)]...)
		submitted[block.Input.Index] = entered
		// An option loaded through dialog_suggestion posts its choice;
		// dialog_submission reports only its value, as Slack's does.
		submission[block.Input.BlockID] = externalChoiceValue(firstValue(entered))
	}
	result, err := h.Messages.SubmitDialog(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), dialogID, submission, h.responseBaseURL(r))
	if err != nil {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadGateway, ModalSubmitted: submitted,
			ModalErrors: map[string]string{"": modalInteractionError(err)},
		})
		return
	}
	if len(result.Errors) != 0 {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusUnprocessableEntity, ModalSubmitted: submitted, ModalErrors: result.Errors,
		})
		return
	}
	if result.Pending {
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusAccepted, ModalSubmitted: submitted,
			Notice: "The app is checking that dialog. Its response will update this page.",
		})
		return
	}
	http.Redirect(w, r, appURL(string(h.requestChannel(r)), "", "", "", ""), http.StatusSeeOther)
}

func (h Handler) dialogClose(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "That app dialog could not be closed. Reload the workspace and try again.")
	if !ok {
		return
	}
	dialogID := domain.DialogID(strings.TrimSpace(fields["dialog_id"]))
	if err := h.Messages.CancelDialog(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), dialogID, h.responseBaseURL(r)); err != nil {
		current, currentErr := h.Messages.CurrentDialog(r.Context(), principal.WorkspaceID, principal.UserID)
		if errors.Is(currentErr, store.ErrNotFound) || (currentErr == nil && current.ID != dialogID) {
			h.renderModalResult(w, r, principal, composerState{
				Status: http.StatusOK, Notice: "The dialog closed, but its app could not be notified.",
			})
			return
		}
		h.renderModalResult(w, r, principal, composerState{
			Status: http.StatusBadGateway, ModalErrors: map[string]string{"": modalInteractionError(err)},
		})
		return
	}
	http.Redirect(w, r, appURL(string(h.requestChannel(r)), "", "", "", ""), http.StatusSeeOther)
}
