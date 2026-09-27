package service

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackobject"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// ErrViewFilesInvalid reports a file_input value the element does not accept:
// a file that is not the submitting member's own upload, more files than
// max_files, or a type outside filetypes. The client checks the same
// constraints first; this is the authoritative check.
var ErrViewFilesInvalid = errors.New("view file input value is invalid")

// fileInputMaxFiles is Slack's default and ceiling for file_input max_files.
const fileInputMaxFiles = 10

// attachViewFiles resolves every file_input entry of a view state. A client
// names the files a member attached by id; each must be the member's own live
// upload in this workspace, and the element's max_files and filetypes must
// admit them. The entry is replaced by the Slack file objects view_submission
// and block_actions carry, and the app that owns the view is granted durable
// read access so files.info and url_private work with its bot token while
// the files stay private to the member.
func (m Messages) attachViewFiles(ctx context.Context, current domain.View, userID domain.UserID, stateJSON string) (string, error) {
	var state struct {
		Values map[string]map[string]map[string]any `json:"values"`
	}
	if json.Unmarshal([]byte(stateJSON), &state) != nil {
		return "", ErrInvalidAppResponse
	}
	var grants []domain.FileAccessGrant
	var grantee domain.UserID
	now := time.Now().UTC()
	origin := slackobject.Origin(m.PublicURL)
	for blockID, actions := range state.Values {
		for actionID, action := range actions {
			if strings.TrimSpace(stringValue(action["type"])) != "file_input" {
				continue
			}
			element, found := viewElement(current.Payload, blockID, actionID)
			if !found || stringValue(element["type"]) != "file_input" {
				return "", ErrViewFilesInvalid
			}
			raw, _ := action["files"].([]any)
			maxFiles := fileInputMaxFiles
			if value, ok := element["max_files"].(float64); ok && value >= 1 && value <= fileInputMaxFiles {
				maxFiles = int(value)
			}
			if len(raw) > maxFiles {
				return "", ErrViewFilesInvalid
			}
			accepted := make([]any, 0, len(raw))
			seen := make(map[domain.FileID]bool, len(raw))
			for _, entry := range raw {
				object, _ := entry.(map[string]any)
				id := domain.FileID(strings.TrimSpace(stringValue(object["id"])))
				if id == "" || seen[id] {
					return "", ErrViewFilesInvalid
				}
				seen[id] = true
				file, err := m.Store.GetFile(ctx, id)
				if errors.Is(err, store.ErrNotFound) {
					return "", ErrViewFilesInvalid
				}
				if err != nil {
					return "", err
				}
				if file.WorkspaceID != current.WorkspaceID || file.Uploader != userID || file.Deleted ||
					!fileTypeAccepted(file, element["filetypes"]) {
					return "", ErrViewFilesInvalid
				}
				if grantee == "" {
					bot, err := m.Store.GetBotByApp(ctx, current.WorkspaceID, current.AppID)
					if err != nil && !errors.Is(err, store.ErrNotFound) {
						return "", err
					}
					grantee = bot.UserID
				}
				if grantee != "" {
					grants = append(grants, domain.FileAccessGrant{FileID: file.ID, WorkspaceID: file.WorkspaceID, UserID: grantee, GrantedAt: now})
				}
				accepted = append(accepted, slackobject.File(origin, file))
			}
			action["files"] = accepted
		}
	}
	if len(grants) != 0 {
		if err := m.Store.GrantFileAccess(ctx, grants); err != nil {
			return "", err
		}
	}
	if state.Values == nil {
		state.Values = map[string]map[string]map[string]any{}
	}
	encoded, err := json.Marshal(map[string]any{"values": state.Values})
	return string(encoded), err
}

// fileTypeAccepted applies a file_input's filetypes: Slack names them by
// extension ("pdf", "png"), and an element without the list takes any file.
func fileTypeAccepted(file domain.File, raw any) bool {
	list, _ := raw.([]any)
	if len(list) == 0 {
		return true
	}
	fileType, _ := file.FileTypes()
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(file.Name)), ".")
	for _, item := range list {
		wanted := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(stringValue(item))), ".")
		if wanted != "" && (wanted == extension || wanted == strings.ToLower(fileType)) {
			return true
		}
	}
	return false
}

// viewElement finds the element a view's block holds under one action_id.
func viewElement(payload, blockID, actionID string) (map[string]any, bool) {
	var view struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if json.Unmarshal([]byte(payload), &view) != nil {
		return nil, false
	}
	for _, block := range view.Blocks {
		if strings.TrimSpace(stringValue(block["block_id"])) != blockID {
			continue
		}
		var candidates []any
		if elements, ok := block["elements"].([]any); ok {
			candidates = append(candidates, elements...)
		}
		for _, name := range []string{"element", "accessory"} {
			if element, ok := block[name].(map[string]any); ok {
				candidates = append(candidates, element)
			}
		}
		for _, raw := range candidates {
			if element, ok := raw.(map[string]any); ok && strings.TrimSpace(stringValue(element["action_id"])) == actionID {
				return element, true
			}
		}
	}
	return nil, false
}
