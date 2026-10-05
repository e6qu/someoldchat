package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	TargetSectionID string          `json:"target_section_id"`
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
		if err := m.authorizeDocumentChannels(ctx, workspaceID, userID, []domain.ConversationID{channelID}); err != nil {
			return domain.Canvas{}, err
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
	canvas := domain.Canvas{ID: id, WorkspaceID: workspaceID, OwnerID: userID, Title: canvasTitle, DocumentContent: content, Version: 1, CreatedAt: now, UpdatedAt: now}
	event, err := canvasEvent(workspaceID, userID, "canvas.created", id, now)
	if err != nil {
		return domain.Canvas{}, err
	}
	if channelID != "" {
		access := domain.CanvasAccess{CanvasID: id, EntityType: domain.GrantChannel, EntityID: string(channelID), Access: domain.AccessWrite}
		accessEvent, eventErr := canvasEvent(workspaceID, userID, "canvas.access_set", id, now,
			events.String("entity_type", "channel"), events.String("entity_id", string(channelID)), events.String("access", "write"))
		if eventErr != nil {
			return domain.Canvas{}, eventErr
		}
		if err := m.Store.CreateCanvasWithAccess(ctx, canvas, event, access, accessEvent); err != nil {
			return domain.Canvas{}, err
		}
	} else if err := m.Store.CreateCanvas(ctx, canvas, event); err != nil {
		return domain.Canvas{}, err
	}
	return canvas, nil
}

// CreateConversationCanvas creates Slack's singular channel-canvas resource.
// It is deliberately separate from sharing a standalone canvas with a channel:
// a channel may have many shared canvases, but only one channel canvas.
func (m Messages) CreateConversationCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channelID domain.ConversationID, title, documentContent string) (domain.Canvas, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.Canvas{}, err
	}
	if channelID == "" {
		return domain.Canvas{}, domain.ErrInvalidCanvas
	}
	if err := m.authorizeDocumentChannels(ctx, workspaceID, userID, []domain.ConversationID{channelID}); err != nil {
		return domain.Canvas{}, err
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
	canvas := domain.Canvas{ID: id, WorkspaceID: workspaceID, OwnerID: userID, Title: canvasTitle, DocumentContent: content, Version: 1, CreatedAt: now, UpdatedAt: now}
	event, err := canvasEvent(workspaceID, userID, "canvas.created", id, now)
	if err != nil {
		return domain.Canvas{}, err
	}
	accessEvent, err := canvasEvent(workspaceID, userID, "canvas.access_set", id, now,
		events.String("entity_type", "channel_canvas"), events.String("entity_id", string(channelID)), events.String("access", string(domain.AccessWrite)))
	if err != nil {
		return domain.Canvas{}, err
	}
	if err := m.Store.CreateChannelCanvas(ctx, canvas, event, channelID, accessEvent); err != nil {
		return domain.Canvas{}, err
	}
	return canvas, nil
}

func (m Messages) ConversationCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, channelID domain.ConversationID) (domain.Canvas, error) {
	if err := m.authorizeDocumentChannels(ctx, workspaceID, userID, []domain.ConversationID{channelID}); err != nil {
		return domain.Canvas{}, err
	}
	return m.Store.GetChannelCanvas(ctx, workspaceID, channelID)
}

func (m Messages) Canvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) (domain.Canvas, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return domain.Canvas{}, err
	}
	return m.Store.GetCanvas(ctx, workspaceID, id)
}

func (m Messages) CanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) (domain.CanvasAccess, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.CanvasAccess{}, err
	}
	return m.Store.GetCanvasAccess(ctx, id, userID)
}

// CanvasGrants reports who a canvas is shared with. Read access is enough to
// ask: a member who can open a document can already see the people commenting
// on it and the people who edited it, so who else can open it is not a further
// secret — and a member deciding whether to share it needs to know it is not
// already shared.
func (m Messages) CanvasGrants(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) ([]domain.CanvasAccess, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return nil, err
	}
	return m.Store.ListCanvasGrants(ctx, workspaceID, id)
}

func (m Messages) Canvases(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, page domain.PageRequest) (domain.CanvasPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.CanvasPage{}, err
	}
	return m.Store.ListCanvases(ctx, workspaceID, userID, page)
}

func (m Messages) EditCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, changes string) error {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return err
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	var input []canvasChange
	if err := json.Unmarshal([]byte(changes), &input); err != nil || len(input) == 0 || len(input) > 100 {
		return domain.ErrInvalidCanvas
	}
	document, err := decodeCanvasDocument(canvas.DocumentContent)
	if err != nil {
		return err
	}
	for _, change := range input {
		if err := applyCanvasChange(&document, &canvas, change); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	canvas.DocumentContent = string(encoded)
	canvas.Version++
	canvas.UpdatedAt = time.Now().UTC()
	event, err := canvasEvent(workspaceID, userID, "canvas.updated", id, canvas.UpdatedAt)
	if err != nil {
		return err
	}
	return m.Store.UpdateCanvas(ctx, canvas, event)
}

// SaveCanvasMarkdown is the web editor's save: the whole document, written as
// one, becomes the canvas. version is the revision the editor opened, so a save
// from a page another writer has since changed is refused with
// store.ErrConflict instead of silently overwriting their work. Only the
// sections that differ are rewritten, so an unchanged section keeps its ID and
// the comments anchored to it. It answers how many sections changed and writes
// nothing when none did.
func (m Messages) SaveCanvasMarkdown(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, version int64, markdown string) (int, int64, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return 0, 0, err
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return 0, 0, err
	}
	if canvas.Version != version {
		return 0, 0, store.ErrConflict
	}
	changed, err := m.rewriteCanvasMarkdown(ctx, workspaceID, userID, canvas, markdown)
	if err != nil {
		return 0, 0, err
	}
	if changed > 0 {
		version++
	}
	return changed, version, nil
}

// rewriteCanvasMarkdown makes the canvas the markdown given, keeping every
// section that did not change. A section of a kind the markdown cannot spell —
// an app's own — comes back from markdown as prose with its text intact; it
// keeps its kind when its text is unchanged, so writing the document through
// markdown does not turn an app's section into a paragraph.
func (m Messages) rewriteCanvasMarkdown(ctx context.Context, workspaceID domain.WorkspaceID, actor domain.UserID, canvas domain.Canvas, markdown string) (int, error) {
	document, err := decodeCanvasDocument(canvas.DocumentContent)
	if err != nil {
		return 0, err
	}
	next := domain.CanvasMarkdownBlocks(markdown)
	kinds := make(map[string]domain.CanvasSectionType)
	for _, section := range document.Sections {
		if !section.Type.Markdown() {
			kinds[section.Text] = section.Type
		}
	}
	for index := range next {
		if kind, ok := kinds[next[index].Text]; ok && next[index].Type == domain.CanvasSectionMarkdown {
			next[index].Type = kind
		}
	}
	merged, changed := domain.MergeCanvasSections(document.Sections, next)
	if changed == 0 {
		return 0, nil
	}
	for index := range merged {
		if merged[index].ID == "" {
			if merged[index].ID, err = newCanvasSectionID(); err != nil {
				return 0, err
			}
		}
	}
	document.Sections = merged
	encoded, err := json.Marshal(document)
	if err != nil {
		return 0, err
	}
	canvas.DocumentContent = string(encoded)
	canvas.Version++
	canvas.UpdatedAt = time.Now().UTC()
	event, err := canvasEvent(workspaceID, actor, "canvas.updated", canvas.ID, canvas.UpdatedAt)
	if err != nil {
		return 0, err
	}
	if err := m.Store.UpdateCanvas(ctx, canvas, event); err != nil {
		return 0, err
	}
	return changed, nil
}

func (m Messages) DeleteCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID) error {
	// Destroying a canvas is reserved to whoever owns it: a collaborator granted
	// write access may change the document, not remove it from everyone else.
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessOwner); err != nil {
		return err
	}
	now := time.Now().UTC()
	event, err := canvasEvent(workspaceID, userID, "canvas.deleted", id, now)
	if err != nil {
		return err
	}
	return m.Store.DeleteCanvas(ctx, workspaceID, id, event)
}

func (m Messages) SetCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	// Granting access is the strongest operation on a canvas: write access must not
	// be enough to hand the canvas to anyone else.
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessOwner); err != nil {
		return err
	}
	if _, err := m.Store.GetCanvas(ctx, workspaceID, id); err != nil {
		return err
	}
	if err := validateCanvasAccess(access, channelIDs, userIDs); err != nil {
		return err
	}
	if len(channelIDs) > 0 && access == domain.AccessOwner {
		return domain.ErrInvalidCanvas
	}
	if err := m.authorizeDocumentChannels(ctx, workspaceID, userID, channelIDs); err != nil {
		return err
	}
	for _, targetID := range userIDs {
		user, err := m.Store.GetUser(ctx, targetID)
		if err != nil || user.WorkspaceID != workspaceID {
			return store.ErrNotFound
		}
	}
	for _, targetID := range channelIDs {
		event, err := canvasEvent(workspaceID, userID, "canvas.access_set", id, time.Now().UTC(), events.String("entity_type", "channel"), events.String("entity_id", string(targetID)), events.String("access", string(access)))
		if err != nil {
			return err
		}
		if err := m.Store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: domain.GrantChannel, EntityID: string(targetID), Access: access}, event); err != nil {
			return err
		}
	}
	for _, targetID := range userIDs {
		event, err := canvasEvent(workspaceID, userID, "canvas.access_set", id, time.Now().UTC(), events.String("entity_type", "user"), events.String("entity_id", string(targetID)), events.String("access", string(access)))
		if err != nil {
			return err
		}
		if err := m.Store.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: domain.GrantUser, EntityID: string(targetID), Access: access}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) DeleteCanvasAccess(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessOwner); err != nil {
		return err
	}
	if _, err := m.Store.GetCanvas(ctx, workspaceID, id); err != nil {
		return err
	}
	if (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return domain.ErrInvalidCanvas
	}
	for _, targetID := range channelIDs {
		if targetID == "" {
			return domain.ErrInvalidCanvas
		}
	}
	for _, targetID := range userIDs {
		if targetID == "" {
			return domain.ErrInvalidCanvas
		}
	}
	for _, targetID := range channelIDs {
		event, err := canvasEvent(workspaceID, userID, "canvas.access_deleted", id, time.Now().UTC(), events.String("entity_type", "channel"), events.String("entity_id", string(targetID)))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: domain.GrantChannel, EntityID: string(targetID)}, event); err != nil {
			return err
		}
	}
	for _, targetID := range userIDs {
		event, err := canvasEvent(workspaceID, userID, "canvas.access_deleted", id, time.Now().UTC(), events.String("entity_type", "user"), events.String("entity_id", string(targetID)))
		if err != nil {
			return err
		}
		if err := m.Store.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: id, EntityType: domain.GrantUser, EntityID: string(targetID)}, event); err != nil {
			return err
		}
	}
	return nil
}

func (m Messages) LookupCanvasSections(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, criteria string) ([]domain.CanvasSection, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
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
		return nil, domain.ErrInvalidCanvas
	}
	allowed := make(map[domain.CanvasSectionType]struct{}, len(filter.SectionTypes))
	for _, value := range filter.SectionTypes {
		allowed[domain.CanvasSectionType(strings.TrimSpace(value))] = struct{}{}
	}
	result := make([]domain.CanvasSection, 0, len(document.Sections))
	for _, section := range document.Sections {
		if len(allowed) > 0 {
			if _, ok := allowed[section.Type]; !ok {
				if _, ok := allowed[domain.CanvasSectionAnyHeader]; !(ok && section.Type.IsHeader()) {
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

// CommentOnCanvas records a remark against a section. Read access is enough:
// commenting is taking part in a document, not editing it, and a canvas shared
// for review that only its editors could discuss would make review impossible.
//
// The section is not checked against the document. A comment about a paragraph
// that has since been rewritten or removed is still what somebody said — often
// the reason it changed — so an anchor is a record of what was being discussed
// rather than a foreign key.
func (m Messages) CommentOnCanvas(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, sectionID, text string) (domain.CanvasComment, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return domain.CanvasComment{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > domain.CanvasCommentLimit {
		return domain.CanvasComment{}, domain.ErrInvalidCanvas
	}
	identifier, err := domain.PublicID("temp:CC:")
	if err != nil {
		return domain.CanvasComment{}, err
	}
	comment := domain.CanvasComment{
		ID: domain.CanvasCommentID(identifier), CanvasID: id, WorkspaceID: workspaceID,
		SectionID: strings.TrimSpace(sectionID), UserID: userID, Text: text, CreatedAt: time.Now().UTC(),
	}
	event, err := canvasEvent(workspaceID, userID, "canvas.commented", id, comment.CreatedAt, events.String("comment_id", identifier))
	if err != nil {
		return domain.CanvasComment{}, err
	}
	if err := m.Store.CreateCanvasComment(ctx, comment, event); err != nil {
		return domain.CanvasComment{}, err
	}
	return comment, nil
}

// CanvasComments reads a canvas's remarks, oldest first, which is how a
// conversation reads.
func (m Messages) CanvasComments(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasCommentPage, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return domain.CanvasCommentPage{}, err
	}
	return m.Store.ListCanvasComments(ctx, workspaceID, userID, id, request)
}

// DeleteCanvasComment removes a remark. Its author alone may: a comment is a
// thing somebody said, and an editor who could delete what others said about
// their document would make the comments worth less than silence.
func (m Messages) DeleteCanvasComment(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasCommentID) error {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return err
	}
	event, err := newEvent(workspaceID, userID, events.NewPayload("canvas.comment_deleted", events.String("comment_id", string(id))), time.Now().UTC())
	if err != nil {
		return err
	}
	return m.Store.DeleteCanvasComment(ctx, workspaceID, id, userID, event)
}

// CanvasRevisions reads what a canvas said before, newest first. Read access is
// enough: a member who may read a canvas may read what it used to say, because
// the history is the same document at an earlier moment and withholding it
// would be withholding content they can already see the successor of.
func (m Messages) CanvasRevisions(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, request domain.PageRequest) (domain.CanvasRevisionPage, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessRead); err != nil {
		return domain.CanvasRevisionPage{}, err
	}
	return m.Store.ListCanvasRevisions(ctx, workspaceID, userID, id, request)
}

// RestoreCanvasRevision puts an earlier revision back. It is an ordinary edit
// rather than a rewind: the current content becomes a revision of its own, so
// restoring the wrong one is itself undoable, and the version keeps counting
// forward. A history you can fall out of is worse than none.
func (m Messages) RestoreCanvasRevision(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, id domain.CanvasID, version int64) (domain.Canvas, error) {
	if err := m.requireCanvasAccess(ctx, workspaceID, userID, id, domain.AccessWrite); err != nil {
		return domain.Canvas{}, err
	}
	page, err := m.Store.ListCanvasRevisions(ctx, workspaceID, userID, id, domain.PageRequest{Limit: domain.CanvasRevisionLimit})
	if err != nil {
		return domain.Canvas{}, err
	}
	var wanted domain.CanvasRevision
	for _, revision := range page.Revisions {
		if revision.Version == version {
			wanted = revision
			break
		}
	}
	if wanted.CanvasID == "" {
		return domain.Canvas{}, domain.ErrInvalidCanvas
	}
	canvas, err := m.Store.GetCanvas(ctx, workspaceID, id)
	if err != nil {
		return domain.Canvas{}, err
	}
	canvas.Title = wanted.Title
	canvas.DocumentContent = wanted.DocumentContent
	canvas.Version++
	canvas.UpdatedAt = time.Now().UTC()
	event, err := canvasEvent(workspaceID, userID, "canvas.restored", id, canvas.UpdatedAt, events.String("restored_version", strconv.FormatInt(version, 10)))
	if err != nil {
		return domain.Canvas{}, err
	}
	if err := m.Store.UpdateCanvas(ctx, canvas, event); err != nil {
		return domain.Canvas{}, err
	}
	return canvas, nil
}

// SearchCanvases answers the Canvases tab. It reuses the one query parser every
// other search uses, so `from:@ada`, `-word` and a quoted phrase mean here what
// they mean in Messages and Files; a canvas-only dialect would be a surprise
// rather than a feature.
//
// Modifiers the object model cannot answer are refused rather than ignored. A
// canvas is not in a conversation — a channel canvas is reached through its
// channel and a standalone one through a grant — so `in:#general` has no
// meaning here, and silently dropping it would return results that look like an
// answer to a question nobody asked.
func (m Messages) SearchCanvases(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, request domain.CanvasSearchRequest) (domain.CanvasPage, error) {
	if err := m.authorizeWorkspace(ctx, workspaceID, userID); err != nil {
		return domain.CanvasPage{}, err
	}
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || utf8.RuneCountInString(request.Query) > 500 {
		return domain.CanvasPage{}, domain.ErrInvalidSearch
	}
	if err := store.CheckAscendingPage(request.Page); err != nil {
		return domain.CanvasPage{}, err
	}
	sortOrder, direction, err := domain.NormalizeSearchOrder(string(request.Sort), string(request.Direction))
	if err != nil {
		return domain.CanvasPage{}, domain.ErrInvalidSearch
	}
	parsed, err := parseSearchQuery(request.Query, m.searchClockFor(ctx, workspaceID, userID))
	if err != nil {
		return domain.CanvasPage{}, domain.ErrInvalidSearch
	}
	if parsed.conversation != "" || parsed.excludedConversation != "" {
		return domain.CanvasPage{}, domain.ErrInvalidSearch
	}
	search := domain.CanvasSearch{
		Terms: parsed.terms, ExcludedTerms: parsed.excludedTerms,
		After: parsed.after, Before: parsed.before,
		Sort: sortOrder, Direction: direction, Page: request.Page,
	}
	if parsed.author != "" {
		search.Owner = m.resolveSearchUser(ctx, workspaceID, userID, parsed.author)
	}
	if parsed.excludedAuthor != "" {
		search.ExcludedOwner = m.resolveSearchUser(ctx, workspaceID, userID, parsed.excludedAuthor)
	}
	return m.Store.SearchCanvases(ctx, workspaceID, userID, search)
}

func validateCanvasAccess(access domain.AccessLevel, channelIDs []domain.ConversationID, userIDs []domain.UserID) error {
	if !access.Valid() || (len(channelIDs) == 0) == (len(userIDs) == 0) {
		return domain.ErrInvalidCanvas
	}
	return nil
}

// canvasEvent builds a canvas journal record. It reports an error instead of
// panicking on a failed identifier draw: a random-source failure is a handled
// condition, and a panic here would surface as an unhandled HTTP 500.
func canvasEvent(workspaceID domain.WorkspaceID, actorID domain.UserID, topic string, id domain.CanvasID, createdAt time.Time, fields ...events.Field) (events.Event, error) {
	fields = append([]events.Field{events.String("canvas_id", string(id))}, fields...)
	return newEvent(workspaceID, actorID, events.NewPayload(topic, fields...), createdAt)
}

func normalizeCanvasContent(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		encoded, err := json.Marshal(canvasDocument{Sections: []domain.CanvasSection{}})
		return string(encoded), err
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(value), &raw); err != nil || raw == nil {
		return "", domain.ErrInvalidCanvas
	}
	sections, err := canvasSectionsFromContent(raw)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(canvasDocument{Sections: sections})
	return string(encoded), err
}

// canvasSectionsFromContent turns one document_content object into the
// sections it stands for. Markdown is split the way Slack splits it —
// headings become header sections and the prose between them markdown
// sections (domain.CanvasMarkdownBlocks) — so a document written with
// "# Heading" shows a heading and is found by canvases.sections.lookup. A
// section of any other kind (an app's own, or a heading written as one) is
// kept as the single section it was sent as.
func canvasSectionsFromContent(raw map[string]any) ([]domain.CanvasSection, error) {
	kind := domain.CanvasSectionType(stringValue(raw["type"]))
	text := stringValue(raw["markdown"])
	if text == "" {
		text = stringValue(raw["text"])
	}
	sections := []domain.CanvasSection{{Type: kind, Text: text}}
	if kind == domain.CanvasSectionMarkdown || kind == "" {
		if blocks := domain.CanvasMarkdownBlocks(text); len(blocks) > 0 {
			sections = blocks
		}
	}
	for index := range sections {
		id, err := newCanvasSectionID()
		if err != nil {
			return nil, err
		}
		sections[index].ID = id
	}
	return sections, nil
}

func decodeCanvasDocument(value string) (canvasDocument, error) {
	var document canvasDocument
	if err := json.Unmarshal([]byte(value), &document); err != nil {
		return canvasDocument{}, domain.ErrInvalidCanvas
	}
	return document, nil
}

func applyCanvasChange(document *canvasDocument, canvas *domain.Canvas, change canvasChange) error {
	if change.Operation == "" {
		return domain.ErrInvalidCanvas
	}
	if len(change.TitleContent) > 0 {
		var title struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(change.TitleContent, &title); err != nil || title.Title == "" {
			return domain.ErrInvalidCanvas
		}
		canvas.Title = strings.TrimSpace(title.Title)
		return nil
	}
	newSections := func() ([]domain.CanvasSection, error) {
		var raw map[string]any
		if err := json.Unmarshal(change.DocumentContent, &raw); err != nil || raw == nil {
			return nil, domain.ErrInvalidCanvas
		}
		return canvasSectionsFromContent(raw)
	}
	if change.Operation == "delete" {
		if change.SectionID == "" {
			return domain.ErrInvalidCanvas
		}
		for index, section := range document.Sections {
			if section.ID == change.SectionID {
				document.Sections = append(document.Sections[:index], document.Sections[index+1:]...)
				return nil
			}
		}
		return store.ErrNotFound
	}
	// move reorders an existing section without minting a new one, so its
	// identity — and the comment anchors that point at it — survive the move. It
	// is composed only by first-party clients (the block editor's up/down
	// controls); Slack's own canvases.edit has no move, and none is exposed at
	// the Slack API boundary.
	if change.Operation == "move_before" || change.Operation == "move_after" {
		if change.SectionID == "" || change.TargetSectionID == "" || change.SectionID == change.TargetSectionID {
			return domain.ErrInvalidCanvas
		}
		moved, ok := domain.CanvasSection{}, false
		remaining := document.Sections[:0:0]
		for _, section := range document.Sections {
			if section.ID == change.SectionID {
				moved, ok = section, true
				continue
			}
			remaining = append(remaining, section)
		}
		if !ok {
			return store.ErrNotFound
		}
		for index, section := range remaining {
			if section.ID == change.TargetSectionID {
				position := index
				if change.Operation == "move_after" {
					position++
				}
				remaining = append(remaining, domain.CanvasSection{})
				copy(remaining[position+1:], remaining[position:])
				remaining[position] = moved
				document.Sections = remaining
				return nil
			}
		}
		return store.ErrNotFound
	}
	sections, err := newSections()
	if err != nil {
		return err
	}
	// insertAt places every section the content stands for at position, in
	// order: one markdown document with a heading is several sections.
	insertAt := func(position int, replaced int) {
		rest := append([]domain.CanvasSection(nil), document.Sections[position+replaced:]...)
		document.Sections = append(append(document.Sections[:position], sections...), rest...)
	}
	switch change.Operation {
	case "insert_at_start":
		insertAt(0, 0)
	case "insert_at_end":
		insertAt(len(document.Sections), 0)
	case "insert_before", "insert_after":
		for index, existing := range document.Sections {
			if existing.ID == change.SectionID {
				position := index
				if change.Operation == "insert_after" {
					position++
				}
				insertAt(position, 0)
				return nil
			}
		}
		return store.ErrNotFound
	case "replace":
		if change.SectionID == "" {
			document.Sections = sections
			return nil
		}
		for index, existing := range document.Sections {
			if existing.ID == change.SectionID {
				insertAt(index, 1)
				return nil
			}
		}
		return store.ErrNotFound
	default:
		return domain.ErrInvalidCanvas
	}
	return nil
}

// newCanvasSectionID draws a section identifier. It reports an error for the
// same reason canvasEvent does: a random-source failure is a handled condition
// reachable from canvases.create and canvases.edit, and the panic that used to
// stand here surfaced as an unhandled HTTP 500 four lines below the comment
// forbidding exactly that.
func newCanvasSectionID() (string, error) {
	value, err := domain.PublicID("temp:C:")
	if err != nil {
		return "", fmt.Errorf("generate canvas section ID: %w", err)
	}
	return value, nil
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}
