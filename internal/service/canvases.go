package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type canvasDocument struct {
	Sections []domain.CanvasSection `json:"sections"`
}

type canvasChange struct {
	Operation       string          `json:"operation"`
	SectionID       string          `json:"section_id"`
	DocumentContent json.RawMessage `json:"document_content"`
	TitleContent    json.RawMessage `json:"title_content"`
}

type canvasCriteria struct {
	SectionTypes []string `json:"section_types"`
	ContainsText string   `json:"contains_text"`
}

func (m Messages) CreateCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, title, documentContent string, channelID domain.ConversationID) (domain.Canvas, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Canvas{}, err
	}
	if channelID != "" {
		conversation, err := m.Store.GetConversation(ctx, channelID)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return domain.Canvas{}, store.ErrNotFound
		}
	}
	content, err := normalizeCanvasContent(documentContent)
	if err != nil {
		return domain.Canvas{}, err
	}
	id, err := domain.NewCanvasID()
	if err != nil {
		return domain.Canvas{}, err
	}
	now := time.Now().UTC()
	canvasTitle := strings.TrimSpace(title)
	if canvasTitle == "" {
		canvasTitle = "Untitled"
	}
	canvas := domain.Canvas{ID: id, WorkspaceID: workspaceID, OwnerID: userID, Title: canvasTitle, DocumentContent: content, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.CreateCanvas(ctx, canvas, canvasEvent(workspaceID, "canvas.created", string(id), now)); err != nil {
		return domain.Canvas{}, err
	}
	if channelID != "" {
		if err := m.SetCanvasAccess(ctx, workspaceID, userID, id, "write", []domain.ConversationID{channelID}, nil); err != nil {
			cleanupErr := m.Store.DeleteCanvas(ctx, workspaceID, id, canvasEvent(workspaceID, "canvas.create_reverted", string(id), time.Now().UTC()))
			return domain.Canvas{}, errors.Join(err, cleanupErr)
		}
	}
	return canvas, nil
}

func (m Messages) EditCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, changes string) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	var input []canvasChange
	if err := json.Unmarshal([]byte(changes), &input); err != nil || len(input) != 1 {
		return ErrInvalidCanvas
	}
	document, err := decodeCanvasDocument(canvas.DocumentContent)
	if err != nil {
		return err
	}
	if err := applyCanvasChange(&document, &canvas, input[0]); err != nil {
		return err
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	canvas.DocumentContent = string(encoded)
	canvas.UpdatedAt = time.Now().UTC()
	return m.Store.UpdateCanvas(ctx, canvas, canvasEvent(workspaceID, "canvas.updated", string(id), canvas.UpdatedAt))
}

func (m Messages) DeleteCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	return m.Store.DeleteCanvas(ctx, workspaceID, id, canvasEvent(workspaceID, "canvas.deleted", string(id), now))
}

func (m Messages) SetCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if _, err := m.Store.GetCanvas(ctx, workspaceID, id); err != nil {
		return err
	}
	if err := validateCanvasAccess(access, channelIDs, userIDs); err != nil {
		return err
	}
	if len(channelIDs) > 0 && access == "owner" {
		return ErrInvalidCanvas
	}
	for _, channelID := range channelIDs {
		conversation, err := m.Store.GetConversation(ctx, channelID)
		if err != nil || conversation.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, targetID := range userIDs {
		user, err := m.Store.GetUser(ctx, targetID)
		if err != nil || user.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, targetID := range channelIDs {
		if err := m.Store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: "channel", EntityID: string(targetID), Access: access}, canvasEvent(workspaceID, "canvas.access_set", string(id)+"|channel|"+string(targetID), time.Now().UTC())); err != nil {
			return err
		}
	}
	for _, targetID := range userIDs {
		if err := m.Store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: "user", EntityID: string(targetID), Access: access}, canvasEvent(workspaceID, "canvas.access_set", string(id)+"|user|"+string(targetID), time.Now().UTC())); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) DeleteCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	if _, err := m.Store.GetCanvas(ctx, workspaceID, id); err != nil {
		return err
	}
	if (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidCanvas
	}
	for _, targetID := range channelIDs {
		if targetID == "" {
			return ErrInvalidCanvas
		}
	}
	for _, targetID := range userIDs {
		if targetID == "" {
			return ErrInvalidCanvas
		}
	}
	for _, targetID := range channelIDs {
		if err := m.Store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: "channel", EntityID: string(targetID)}, canvasEvent(workspaceID, "canvas.access_deleted", string(id)+"|channel|"+string(targetID), time.Now().UTC())); err != nil {
			return err
		}
	}
	for _, targetID := range userIDs {
		if err := m.Store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: "user", EntityID: string(targetID)}, canvasEvent(workspaceID, "canvas.access_deleted", string(id)+"|user|"+string(targetID), time.Now().UTC())); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) LookupCanvasSections(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, criteria string) ([]domain.CanvasSection, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return nil, err
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return nil, err
	}
	document, err := decodeCanvasDocument(canvas.DocumentContent)
	if err != nil {
		return nil, err
	}
	var filter canvasCriteria
	if err := json.Unmarshal([]byte(criteria), &filter); err != nil {
		return nil, ErrInvalidCanvas
	}
	allowed := make(map[string]struct{}, len(filter.SectionTypes))
	for _, value := range filter.SectionTypes {
		allowed[strings.TrimSpace(value)] = struct{}{}
	}
	result := make([]domain.CanvasSection, 0, len(document.Sections))
	for _, section := range document.Sections {
		if len(allowed) > 0 {
			if _, ok := allowed[section.Type]; !ok {
				if _, ok := allowed["any_header"]; !(ok && strings.HasPrefix(section.Type, "h")) {
					continue
				}
			}
		}
		if filter.ContainsText != "" && !strings.Contains(section.Text, filter.ContainsText) {
			continue
		}
		result = append(result, section)
	}
	return result, nil
}

func validateCanvasAccess(access string, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if access != "read" && access != "write" && access != "owner" || (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return ErrInvalidCanvas
	}
	return nil
}

func canvasEvent(workspaceID domain.WorkspaceID, topic, payload string, createdAt time.Time) events.Event {
	id, err := domain.NewEventID()
	if err != nil {
		panic(fmt.Sprintf("generate canvas event ID: %v", err))
	}
	return events.Event{ID: id, WorkspaceID: workspaceID, Topic: topic, Payload: payload, CreatedAt: createdAt}
}

func normalizeCanvasContent(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		encoded, err := json.Marshal(canvasDocument{Sections: []domain.CanvasSection{}})
		return string(encoded), err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(value), &raw); err != nil || raw == nil {
		return "", ErrInvalidCanvas
	}
	section := domain.CanvasSection{ID: newCanvasSectionID(), Type: stringValue(raw["type"]), Text: stringValue(raw["markdown"])}
	if section.Text == "" {
		section.Text = stringValue(raw["text"])
	}
	encoded, err := json.Marshal(canvasDocument{Sections: []domain.CanvasSection{section}})
	return string(encoded), err
}

func decodeCanvasDocument(value string) (canvasDocument, error) {
	var document canvasDocument
	if err := json.Unmarshal([]byte(value), &document); err != nil {
		return canvasDocument{}, ErrInvalidCanvas
	}
	return document, nil
}

func applyCanvasChange(document *canvasDocument, canvas *domain.Canvas, change canvasChange) error {
	if change.Operation == "" {
		return ErrInvalidCanvas
	}
	if len(change.TitleContent) > 0 {
		var title struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(change.TitleContent, &title); err != nil || title.Title == "" {
			return ErrInvalidCanvas
		}
		canvas.Title = strings.TrimSpace(title.Title)
		return nil
	}
	newSection := func() (domain.CanvasSection, error) {
		var raw map[string]any
		if err := json.Unmarshal(change.DocumentContent, &raw); err != nil || raw == nil {
			return domain.CanvasSection{}, ErrInvalidCanvas
		}
		text := stringValue(raw["markdown"])
		if text == "" {
			text = stringValue(raw["text"])
		}
		return domain.CanvasSection{ID: newCanvasSectionID(), Type: stringValue(raw["type"]), Text: text}, nil
	}
	if change.Operation == "delete" {
		if change.SectionID == "" {
			return ErrInvalidCanvas
		}
		for index, section := range document.Sections {
			if section.ID == change.SectionID {
				document.Sections = append(document.Sections[:index], document.Sections[index+1:]...)
				return nil
			}
		}
		return store.ErrNotFound
	}
	section, err := newSection()
	if err != nil {
		return err
	}
	switch change.Operation {
	case "insert_at_start":
		document.Sections = append([]domain.CanvasSection{section}, document.Sections...)
	case "insert_at_end":
		document.Sections = append(document.Sections, section)
	case "insert_before", "insert_after":
		for index, existing := range document.Sections {
			if existing.ID == change.SectionID {
				position := index
				if change.Operation == "insert_after" {
					position++
				}
				document.Sections = append(document.Sections, domain.CanvasSection{})
				copy(document.Sections[position+1:], document.Sections[position:])
				document.Sections[position] = section
				return nil
			}
		}
		return store.ErrNotFound
	case "replace":
		if change.SectionID == "" {
			document.Sections = []domain.CanvasSection{section}
			return nil
		}
		for index, existing := range document.Sections {
			if existing.ID == change.SectionID {
				document.Sections[index] = section
				return nil
			}
		}
		return store.ErrNotFound
	default:
		return ErrInvalidCanvas
	}
	return nil
}

func newCanvasSectionID() string {
	value, err := domain.PublicID("temp:C:")
	if err != nil {
		panic(fmt.Sprintf("generate canvas section ID: %v", err))
	}
	return value
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
