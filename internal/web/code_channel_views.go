package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A code channel shows its agent's work above the timeline: the context bar
// the agent keeps with agents.conversations.setProperties, and a tab for each
// view it renders with agents.conversations.setView.

// codeChannelView is a code channel's header region.
type codeChannelView struct {
	Present    bool
	ContextBar []codeChannelContextItemView
	Resource   domain.AgentResource
	Tabs       []codeChannelTabView
}

type codeChannelContextItemView struct {
	Label string
	URL   string
}

type codeChannelTabView struct {
	ID      string
	Label   string
	URL     string
	Current bool
}

// codeChannelPaneView is the open view tab's content: exactly one of its
// kinds is filled.
type codeChannelPaneView struct {
	Label string
	Type  string
	// FrameURL is an html view's sandboxed document.
	FrameURL   string
	Diff       []codeChannelDiffLine
	BaseBranch string
	HeadBranch string
	Blocks     []messageBlockView
	CanvasURL  string
	PRURL      string
}

// codeChannelDiffLine is one line of a unified diff with its kind: add,
// remove, hunk, file, or context.
type codeChannelDiffLine struct {
	Kind string
	Text string
}

// newCodeChannelView reads the open conversation's code channel record and
// views. A conversation that is not a code channel, or one the read fails
// for, shows no code channel region: the timeline is still the conversation.
func (h Handler) newCodeChannelView(r *http.Request, principal auth.Principal, channel domain.ConversationID, openView string) (codeChannelView, *codeChannelPaneView) {
	record, err := h.Messages.CodeChannel(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		return codeChannelView{}, nil
	}
	view := codeChannelView{Present: true, Resource: record.AgentResource}
	for _, item := range record.ContextBar {
		label := item.Label
		if label == "" {
			label = item.Key
		}
		view.ContextBar = append(view.ContextBar, codeChannelContextItemView{Label: label, URL: item.URL})
	}
	views, err := h.Messages.CodeChannelViews(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		return view, nil
	}
	var pane *codeChannelPaneView
	for _, tab := range views {
		current := string(tab.ID) == openView
		view.Tabs = append(view.Tabs, codeChannelTabView{
			ID: string(tab.ID), Label: tab.Label, Current: current,
			URL: "/app?channel=" + url.QueryEscape(string(channel)) + "&tab=view&view=" + url.QueryEscape(string(tab.ID)),
		})
		if current {
			pane = newCodeChannelPaneView(channel, tab)
		}
	}
	return view, pane
}

func newCodeChannelPaneView(channel domain.ConversationID, view domain.CodeChannelView) *codeChannelPaneView {
	pane := &codeChannelPaneView{Label: view.Label, Type: string(view.Type)}
	switch view.Type {
	case domain.CodeChannelViewHTML:
		// The version keeps a browser from showing a cached document after
		// the agent updates the view.
		pane.FrameURL = "/app/code-views/content?channel=" + url.QueryEscape(string(channel)) + "&view=" + url.QueryEscape(string(view.ID)) + "&v=" + strconv.FormatInt(view.Version, 10)
	case domain.CodeChannelViewDiff:
		pane.Diff = codeChannelDiffLines(view.Content)
		pane.BaseBranch, pane.HeadBranch = view.BaseBranch, view.HeadBranch
	case domain.CodeChannelViewBlockKit:
		pane.Blocks = decodeMessageBlocks(view.Blocks)
	case domain.CodeChannelViewCanvas:
		pane.CanvasURL = "/app/canvases/" + url.PathEscape(string(view.CanvasID))
	case domain.CodeChannelViewPullRequest:
		pane.PRURL = view.PRURL
		pane.BaseBranch, pane.HeadBranch = view.BaseBranch, view.HeadBranch
	}
	return pane
}

// codeChannelDiffLines classifies a unified diff's lines for display.
func codeChannelDiffLines(diff string) []codeChannelDiffLine {
	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	out := make([]codeChannelDiffLine, 0, len(lines))
	for _, line := range lines {
		kind := "context"
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, "diff "), strings.HasPrefix(line, "index "):
			kind = "file"
		case strings.HasPrefix(line, "@@"):
			kind = "hunk"
		case strings.HasPrefix(line, "+"):
			kind = "add"
		case strings.HasPrefix(line, "-"):
			kind = "remove"
		}
		out = append(out, codeChannelDiffLine{Kind: kind, Text: line})
	}
	return out
}

// codeViewResourceOrigins is the curated allowlist an html view may load
// scripts, styles, fonts, images and media from; each view's declared
// resource_domains are added to it. Slack does not publish its list.
var codeViewResourceOrigins = []string{
	"https://cdnjs.cloudflare.com", "https://cdn.jsdelivr.net", "https://unpkg.com",
	"https://fonts.googleapis.com", "https://fonts.gstatic.com",
}

// codeViewContentSecurityPolicy is an html view's policy. The sandbox
// directive gives the document an opaque origin wherever it is opened, so it
// can reach neither the member's session nor this origin's API; it may run
// its own inline script and load from the allowlist, and may not connect
// anywhere, because connect_domains is accepted but not honored yet.
func codeViewContentSecurityPolicy(csp domain.CodeChannelViewCSP) string {
	origins := strings.Join(append(append([]string{}, codeViewResourceOrigins...), csp.ResourceDomains...), " ")
	return "sandbox allow-scripts; default-src 'none'; script-src 'unsafe-inline' " + origins +
		"; style-src 'unsafe-inline' " + origins + "; font-src " + origins + "; img-src data: blob: " + origins +
		"; media-src data: blob: " + origins + "; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"
}

// codeViewContent serves an html view's document into the channel's
// sandboxed frame.
func (h Handler) codeViewContent(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	id := domain.CodeChannelViewID(strings.TrimSpace(r.URL.Query().Get("view")))
	views, err := h.Messages.CodeChannelViews(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if errors.Is(err, domain.ErrNotCodeChannel) {
		h.writeMissingCodeView(w)
		return
	}
	if err != nil {
		h.writeFragmentError(w, err, "That view is temporarily unavailable.")
		return
	}
	for _, view := range views {
		if view.ID != id || view.Type != domain.CodeChannelViewHTML {
			continue
		}
		header := w.Header()
		header.Set("Content-Security-Policy", codeViewContentSecurityPolicy(view.CSP))
		header.Set("X-Frame-Options", "SAMEORIGIN")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(view.Content))
		return
	}
	h.writeMissingCodeView(w)
}

func (h Handler) writeMissingCodeView(w http.ResponseWriter) {
	secureHeaders(w, workspaceContentSecurityPolicy())
	http.Error(w, "that view is not available", http.StatusNotFound)
}
