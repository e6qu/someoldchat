package domain

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// A code channel view is a tab an agent renders in its code channel with
// agents.conversations.setView: a self-contained HTML document, a unified
// diff, Block Kit, a canvas, or a pull request. An agent names a view by its
// own key, so setting the same key again updates the same tab; a diff view is
// the channel's one diff. Each content-bearing view is versioned, and the
// version advances on every write.

// CodeChannelViewType is a view's kind.
type CodeChannelViewType string

const (
	CodeChannelViewHTML        CodeChannelViewType = "html"
	CodeChannelViewDiff        CodeChannelViewType = "diff"
	CodeChannelViewBlockKit    CodeChannelViewType = "block_kit"
	CodeChannelViewCanvas      CodeChannelViewType = "canvas"
	CodeChannelViewPullRequest CodeChannelViewType = "pull_request"
)

// Valid reports one of the five kinds the SDK declares.
func (t CodeChannelViewType) Valid() bool {
	switch t {
	case CodeChannelViewHTML, CodeChannelViewDiff, CodeChannelViewBlockKit, CodeChannelViewCanvas, CodeChannelViewPullRequest:
		return true
	}
	return false
}

// CodeChannelCanvasAccess is the access a canvas view grants the channel to
// its canvas: write, or comment — read and comment, so the agent stays the
// canvas's only author.
type CodeChannelCanvasAccess string

const (
	CodeChannelCanvasWrite   CodeChannelCanvasAccess = "write"
	CodeChannelCanvasComment CodeChannelCanvasAccess = "comment"
)

// Grant is the canvas access level the channel's members receive: commenting
// is what a reader may do here.
func (a CodeChannelCanvasAccess) Grant() AccessLevel {
	if a == CodeChannelCanvasComment {
		return AccessRead
	}
	return AccessWrite
}

// CodeChannelViewContentLimit is the declared cap on html and diff content;
// larger content is content_too_large.
const CodeChannelViewContentLimit = 1_000_000

// codeChannelDiffKey is the view key of a channel's one diff view.
const codeChannelDiffKey = "diff"

// CodeChannelView is one view tab of a code channel.
type CodeChannelView struct {
	WorkspaceID  WorkspaceID
	Conversation ConversationID
	// ID is the tab's identifier and FileID the file the view's content is
	// published as; both are assigned when the view is first set and kept.
	ID     CodeChannelViewID
	FileID FileID
	// Key is the agent's name for the view, its upsert key.
	Key   string
	Type  CodeChannelViewType
	Label string
	// AppID and BotUserID are the agent that last set the view.
	AppID     AppID
	BotUserID UserID
	// Content is the html document or the unified diff.
	Content string
	// Blocks is a block_kit view's normalized blocks.
	Blocks string
	// CanvasID and AccessLevel are a canvas view's canvas and the access the
	// channel was granted to it; AgentContentHash is opaque to the server.
	CanvasID         CanvasID
	AccessLevel      CodeChannelCanvasAccess
	AgentContentHash string
	// PRURL is a pull_request view's pull request.
	PRURL      string
	BaseBranch string
	HeadBranch string
	CSP        CodeChannelViewCSP
	// Version advances on every write.
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CodeChannelViewCSP is a view's Content-Security-Policy domain declarations.
type CodeChannelViewCSP struct {
	ConnectDomains  []string `json:"connect_domains,omitempty"`
	ResourceDomains []string `json:"resource_domains,omitempty"`
}

// CodeChannelViewRequest is agents.conversations.setView's arguments.
type CodeChannelViewRequest struct {
	Type             CodeChannelViewType
	Key              string
	Name             string
	Content          string
	Blocks           string
	CanvasID         CanvasID
	AccessLevel      CodeChannelCanvasAccess
	AgentContentHash string
	PRURL            string
	BaseBranch       string
	HeadBranch       string
	CSP              CodeChannelViewCSP
}

// The setView and removeView refusals.
var (
	// ErrInvalidCodeChannelView is a view argument outside its declared
	// shape (invalid_arguments).
	ErrInvalidCodeChannelView = errors.New("invalid code channel view argument")
	// ErrCodeChannelViewTooLarge is html or diff content over the cap
	// (content_too_large).
	ErrCodeChannelViewTooLarge = errors.New("code channel view content is too large")
	// ErrCodeChannelViewNotFound is a view the channel does not have
	// (view_not_found).
	ErrCodeChannelViewNotFound = errors.New("code channel view not found")
	// ErrCodeChannelViewCanvasNotFound is a canvas view's canvas that does
	// not exist or that the agent cannot share (canvas_not_found).
	ErrCodeChannelViewCanvasNotFound = errors.New("code channel view canvas not found")
)

// NormalizeCodeChannelViewRequest applies setView's defaults and checks what
// each kind requires: html and diff need content, block_kit blocks, canvas a
// canvas, pull_request a pull request URL; every kind but diff needs a key.
// A diff view is the channel's singleton, so its key is fixed.
func NormalizeCodeChannelViewRequest(request CodeChannelViewRequest) (CodeChannelViewRequest, error) {
	if request.Type == "" {
		request.Type = CodeChannelViewHTML
	}
	if !request.Type.Valid() {
		return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
	}
	request.Key = strings.TrimSpace(request.Key)
	request.Name = strings.TrimSpace(request.Name)
	if request.Type == CodeChannelViewDiff {
		request.Key = codeChannelDiffKey
	}
	if request.Key == "" || utf8.RuneCountInString(request.Key) > 255 || utf8.RuneCountInString(request.Name) > 255 {
		return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
	}
	switch request.Type {
	case CodeChannelViewHTML, CodeChannelViewDiff:
		if strings.TrimSpace(request.Content) == "" {
			return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
		}
		if len(request.Content) > CodeChannelViewContentLimit {
			return CodeChannelViewRequest{}, ErrCodeChannelViewTooLarge
		}
		request.Blocks, request.CanvasID, request.PRURL = "", "", ""
	case CodeChannelViewBlockKit:
		if strings.TrimSpace(request.Blocks) == "" {
			return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
		}
		request.Content, request.CanvasID, request.PRURL = "", "", ""
	case CodeChannelViewCanvas:
		if request.CanvasID == "" {
			return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
		}
		if request.AccessLevel == "" {
			request.AccessLevel = CodeChannelCanvasWrite
		}
		if request.AccessLevel != CodeChannelCanvasWrite && request.AccessLevel != CodeChannelCanvasComment {
			return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
		}
		request.Content, request.Blocks, request.PRURL = "", "", ""
	case CodeChannelViewPullRequest:
		if !ValidCodeChannelURL(request.PRURL) {
			return CodeChannelViewRequest{}, ErrInvalidCodeChannelView
		}
		request.Content, request.Blocks, request.CanvasID = "", "", ""
	}
	if request.Type != CodeChannelViewCanvas {
		request.AccessLevel, request.AgentContentHash = "", ""
	}
	if request.Type != CodeChannelViewDiff {
		request.BaseBranch, request.HeadBranch = "", ""
	}
	var err error
	if request.CSP.ConnectDomains, err = normalizeCSPDomains(request.CSP.ConnectDomains); err != nil {
		return CodeChannelViewRequest{}, err
	}
	if request.CSP.ResourceDomains, err = normalizeCSPDomains(request.CSP.ResourceDomains); err != nil {
		return CodeChannelViewRequest{}, err
	}
	return request, nil
}

// CodeChannelViewLabel is the tab label: the name the agent gave, or the last
// path segment of its key without an .html or .htm extension.
func CodeChannelViewLabel(request CodeChannelViewRequest) string {
	if request.Name != "" {
		return request.Name
	}
	if request.Type == CodeChannelViewDiff {
		return "Diff"
	}
	label := path.Base(strings.TrimRight(request.Key, "/"))
	for _, extension := range []string{".html", ".htm"} {
		if strings.HasSuffix(strings.ToLower(label), extension) {
			label = label[:len(label)-len(extension)]
		}
	}
	if label == "" || label == "." {
		return request.Key
	}
	return label
}

// normalizeCSPDomains validates a view's CSP origins: https only, and never a
// private, loopback or internal host, as the SDK documents the server does.
func normalizeCSPDomains(domains []string) ([]string, error) {
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		parsed, err := url.Parse(strings.TrimSpace(domain))
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, ErrInvalidCodeChannelView
		}
		host := parsed.Hostname()
		if privateHost(host) {
			return nil, ErrInvalidCodeChannelView
		}
		normalized = append(normalized, "https://"+parsed.Host)
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

// privateHost reports a loopback, private, link-local or internal host name.
func privateHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsLinkLocalMulticast()
	}
	return false
}

// EncodeCodeChannelViewCSP and DecodeCodeChannelViewCSP are the stored JSON
// form of a view's CSP.
func EncodeCodeChannelViewCSP(csp CodeChannelViewCSP) (string, error) {
	encoded, err := json.Marshal(csp)
	return string(encoded), err
}

func DecodeCodeChannelViewCSP(encoded string) (CodeChannelViewCSP, error) {
	var csp CodeChannelViewCSP
	if strings.TrimSpace(encoded) == "" {
		return csp, nil
	}
	err := json.Unmarshal([]byte(encoded), &csp)
	return csp, err
}
