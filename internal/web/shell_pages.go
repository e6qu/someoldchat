package web

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// returnTarget is the page a form in the frame asked to come back to. The
// frame's forms (status, away, pause, star, mute) are reachable from every
// page, so answering each with the page that owns the mutation — the People
// directory for a status, Notifications for a pause — took the member away
// from what they were doing. Only this application's own /app pages are
// accepted: anything else is an open redirect.
func returnTarget(fields map[string]string, fallback string) string {
	value := strings.TrimSpace(fields["return"])
	if value == "" || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") {
		return fallback
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || (parsed.Path != "/app" && !strings.HasPrefix(parsed.Path, "/app/")) {
		return fallback
	}
	return parsed.RequestURI()
}

// shellFormData is a page that shows one of the frame's dialogs as a page:
// what a link to the dialog opens without script, or when followed directly.
type shellFormData struct {
	Shell shellView
	Title string
	Form  string
}

const shellFormMarkup = `{{define "title"}}{{.Title}} · {{.Shell.WorkspaceName}}{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + `{{end}}
{{define "content"}}<a class="skip-link" href="#content">Skip to the content</a>
<div class="shell" data-channel="{{.Shell.Channel}}">
  {{template "shell-top" .Shell}}
  <div class="workspace without-pane">
    {{template "shell-rail" .Shell}}
    <main class="shell-main" id="content" tabindex="-1">
      <div class="shell-page-form">
        {{if eq .Form "status"}}{{template "status-form" .Shell}}
        {{else if eq .Form "create-channel"}}{{if .Shell.CanCreate}}{{template "create-channel-form" .Shell}}{{else}}<h1>Create a channel</h1><p>Your current permissions do not allow creating channels.</p>{{end}}
        {{else if eq .Form "section"}}<form method="post" action="/app/sidebar/sections/create?channel={{.Shell.Channel}}"><div class="dialog-head"><h1>Create a section</h1></div><div class="dialog-body"><input type="hidden" name="_csrf" value="{{.Shell.CSRFToken}}"><label for="new-section-name">Section name</label><input id="new-section-name" type="text" name="name" maxlength="80" required></div><div class="dialog-foot"><a class="button" href="{{.Shell.HomeURL}}">Cancel</a><button class="button primary" type="submit">Create section</button></div></form>
        {{else}}<div class="dialog-head"><h1>Preferences</h1></div>{{template "preferences-panels" .Shell}}{{end}}
      </div>
    </main>
  </div>
</div>
{{template "shell-dialogs" .Shell}}{{end}}`

var shellFormTemplate = mustPage(shellFormMarkup)

func (h Handler) shellFormPage(title, form string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
		if err != nil {
			h.writeAuthError(w, r, err)
			return
		}
		shell := h.newShell(r, principal, shellRequest{Destination: destinationHome})
		h.writeHTML(w, shellFormTemplate, shellFormData{Shell: shell, Title: title, Form: form}, http.StatusOK, "the page could not be rendered")
	}
}

// setStatus saves the member's status from the status dialog. It is its own
// mutation rather than the profile form's, because the profile form writes the
// display name and photo as well and a status set from the avatar menu must
// not blank either.
func (h Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your status could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your profile is temporarily unavailable.")
		return
	}
	profile := current.Profile
	if fields["clear_status"] != "" {
		profile.StatusText, profile.StatusEmoji, profile.StatusExpiration = "", "", time.Time{}
	} else {
		profile.StatusText = strings.TrimSpace(fields["status_text"])
		profile.StatusEmoji = strings.TrimSpace(fields["status_emoji"])
		if profile.StatusEmoji != "" && !strings.HasPrefix(profile.StatusEmoji, ":") {
			profile.StatusEmoji = ":" + strings.Trim(profile.StatusEmoji, ":") + ":"
		}
		if profile.StatusText != "" && profile.StatusEmoji == "" {
			profile.StatusEmoji = ":speech_balloon:"
		}
		expiration, reason := statusExpiration(fields, time.Now())
		if reason != "" {
			h.writeMutationError(w, r, http.StatusBadRequest, "Your status was not saved", reason)
			return
		}
		profile.StatusExpiration = expiration
	}
	if _, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile); err != nil {
		if errors.Is(err, service.ErrInvalidProfile) {
			h.writeMutationError(w, r, http.StatusBadRequest, "Your status was not saved", "A status is at most 100 characters, and its emoji must be an emoji this workspace knows, written like :palm_tree:.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Your status was not saved", "The workspace store is temporarily unavailable. Try again.")
		return
	}
	h.redirectMutation(w, r, returnTarget(fields, "/app"))
}

// statusExpiration turns the dialog's "Clear after" choice into an instant.
// "Today" and "This week" end in the member's own time zone, which the dialog
// sends, because the end of the day is a local fact.
func statusExpiration(fields map[string]string, now time.Time) (time.Time, string) {
	location := time.UTC
	if zone := strings.TrimSpace(fields["timezone"]); zone != "" {
		if loaded, err := time.LoadLocation(zone); err == nil {
			location = loaded
		}
	}
	local := now.In(location)
	switch choice := strings.TrimSpace(fields["clear_after"]); choice {
	case "", "never":
		return time.Time{}, ""
	case "today":
		return time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 0, 0, location).UTC(), ""
	case "week":
		daysLeft := (7 - int(local.Weekday())) % 7
		end := time.Date(local.Year(), local.Month(), local.Day()+daysLeft, 23, 59, 0, 0, location)
		return end.UTC(), ""
	case "custom":
		value, err := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(fields["clear_at"]), location)
		if err != nil || !value.After(now) {
			return time.Time{}, "Choose a date and time in the future for your status to clear."
		}
		return value.UTC(), ""
	default:
		minutes, err := strconv.Atoi(choice)
		if err != nil || minutes <= 0 || minutes > 7*24*60 {
			return time.Time{}, "Choose when your status should clear."
		}
		return now.Add(time.Duration(minutes) * time.Minute).UTC(), ""
	}
}

// starConversation stars or unstars a conversation for the member, which moves
// it into or out of the sidebar's Starred section. Both directions are
// idempotent: starring twice, or unstarring what is not starred, leaves one
// durable outcome rather than an error.
func (h Handler) starConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeStarsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The star could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	if fields["starred"] == "false" {
		err = h.Messages.RemoveStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, "")
		if errors.Is(err, service.ErrNotStarred) {
			err = nil
		}
	} else {
		err = h.Messages.AddStar(r.Context(), principal.WorkspaceID, principal.UserID, channel, "")
		if errors.Is(err, store.ErrAlreadyExists) {
			err = nil
		}
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusNotFound, "That conversation is not available", "Nothing was starred or unstarred.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The star was not changed", "The workspace store is temporarily unavailable. Try again.")
		}
		return
	}
	h.redirectMutation(w, r, returnTarget(fields, appURL(string(channel), "", "", "", "")))
}

// removeConversationMember removes someone from a channel from the member
// list's per-member menu (conversations.kick).
func (h Handler) removeConversationMember(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The member could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	if err := h.Messages.KickConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel, target); err != nil {
		switch {
		case errors.Is(err, service.ErrCannotKickFromDefault):
			h.writeMutationError(w, r, http.StatusForbidden, "Nobody can be removed from this channel", "It is one of the workspace's required channels, which every member belongs to.")
		case errors.Is(err, service.ErrCannotKickSelf):
			h.writeMutationError(w, r, http.StatusBadRequest, "You cannot remove yourself", "Use Leave channel instead.")
		case errors.Is(err, service.ErrNotInConversation), errors.Is(err, service.ErrUserNotFound), errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That person is not in this channel", "Nothing was changed.")
		case errors.Is(err, service.ErrInvalidConversation):
			h.writeMutationError(w, r, http.StatusBadRequest, "Nobody can be removed from a direct message", "A direct message's members are what it is.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The person was not removed", "The workspace store is temporarily unavailable. Nothing was changed.")
		}
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel, detailsTabMembers))
}

// addBookmark adds a link to the channel's bookmarks bar.
func (h Handler) addBookmark(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The bookmark could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	link := strings.TrimSpace(fields["link"])
	if parsed, parseErr := url.Parse(link); parseErr != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The bookmark was not added", "A bookmark needs a web address that starts with https:// or http://.")
		return
	}
	if _, err := h.Messages.AddBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["title"], domain.BookmarkLink, link, "", "", "", ""); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidBookmark):
			h.writeMutationError(w, r, http.StatusBadRequest, "The bookmark was not added", "Give the bookmark a name of at most 255 characters and a link.")
		case errors.Is(err, service.ErrNotInConversation), errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusForbidden, "The bookmark was not added", "Only members of this conversation can add bookmarks to it.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The bookmark was not added", "The workspace store is temporarily unavailable. Try again.")
		}
		return
	}
	h.redirectMutation(w, r, appURL(string(channel), "", "", "", ""))
}

func (h Handler) removeBookmark(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeBookmarksWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The bookmark could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	if err := h.Messages.RemoveBookmark(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.BookmarkID(strings.TrimSpace(fields["bookmark"]))); err != nil && !errors.Is(err, store.ErrNotFound) {
		if errors.Is(err, service.ErrNotInConversation) {
			h.writeMutationError(w, r, http.StatusForbidden, "The bookmark was not removed", "Only members of this conversation can change its bookmarks.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "The bookmark was not removed", "The workspace store is temporarily unavailable. Try again.")
		return
	}
	h.redirectMutation(w, r, appURL(string(channel), "", "", "", ""))
}

// browseChannelsData is Browse channels (Ctrl/Cmd+Shift+L): every channel the
// member can find, searchable, filterable and sortable, with a View action
// that opens a channel in preview and a Join action beside it.
type browseChannelsData struct {
	Shell     shellView
	Query     string
	Type      string
	Sort      string
	HideMine  bool
	Channels  []conversationView
	Truncated bool
	Error     string
	CanJoin   bool
}

const browseChannelsMarkup = `{{define "title"}}Browse channels · {{.Shell.WorkspaceName}}{{end}}
{{define "styles"}}` + shellStyle + shellPageStyle + `{{end}}
{{define "scripts"}}` + shellScript + searchSuggestionsScript + `{{end}}
{{define "content"}}<a class="skip-link" href="#content">Skip to the content</a>
<div class="shell" data-channel="{{.Shell.Channel}}">
  {{template "shell-top" .Shell}}
  <div class="workspace without-pane">
    {{template "shell-rail" .Shell}}
    <main class="shell-main browse-channels" id="content" tabindex="-1">
      <div class="page-head"><h1>Channels</h1>{{if .Shell.CanCreate}}<a class="button" href="{{.Shell.With "/app/channels/new"}}" data-dialog-open="create-channel">Create channel</a>{{end}}</div>
      <form class="browse-filters" method="get" action="/app/channels" role="search" aria-label="Search channels">
        <input type="hidden" name="channel" value="{{.Shell.Channel}}">
        <label class="browse-search"><span class="visually-hidden">Search for channels</span>{{icon "search"}}<input type="search" name="q" value="{{.Query}}" placeholder="Search for channels" autocomplete="off" autofocus></label>
        <label>Channel type<select name="type"><option value="all"{{if eq .Type "all"}} selected{{end}}>All channel types</option><option value="public"{{if eq .Type "public"}} selected{{end}}>Public channels</option><option value="private"{{if eq .Type "private"}} selected{{end}}>Private channels</option><option value="archived"{{if eq .Type "archived"}} selected{{end}}>Archived channels</option></select></label>
        <label>Sort<select name="sort"><option value="name"{{if eq .Sort "name"}} selected{{end}}>A to Z</option><option value="name-desc"{{if eq .Sort "name-desc"}} selected{{end}}>Z to A</option><option value="members"{{if eq .Sort "members"}} selected{{end}}>Most members</option><option value="members-asc"{{if eq .Sort "members-asc"}} selected{{end}}>Fewest members</option><option value="newest"{{if eq .Sort "newest"}} selected{{end}}>Newest channel</option><option value="oldest"{{if eq .Sort "oldest"}} selected{{end}}>Oldest channel</option></select></label>
        <label class="browse-check"><input type="checkbox" name="hide_mine" value="1"{{if .HideMine}} checked{{end}}> Hide my channels</label>
        <button class="button" type="submit">Apply</button>
      </form>
      {{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{end}}
      <p class="browse-count" role="status">{{len .Channels}} result{{if ne (len .Channels) 1}}s{{end}}{{if .Truncated}} (the first {{len .Channels}}; refine the search to find others){{end}}</p>
      <ul class="browse-list">{{range .Channels}}<li class="browse-row">
        <a class="browse-name" href="/app?channel={{.ID}}">{{if .IsPrivate}}{{icon "lock"}}{{else}}{{icon "hash"}}{{end}}<span>{{.Name}}</span></a>
        <p class="browse-meta">{{if .IsMember}}<span class="browse-joined">{{icon "check"}} Joined</span> · {{end}}{{if .Archived}}Archived · {{end}}{{.MemberCount}} member{{if ne .MemberCount 1}}s{{end}}{{if .Topic}} · {{.Topic}}{{end}}</p>
        <div class="browse-actions">
          <a class="button" href="/app?channel={{.ID}}" aria-label="View {{.Name}}">View</a>
          {{if and (not .IsMember) (not .Archived) (not .IsPrivate) $.CanJoin}}<form method="post" action="/app/join?channel={{.ID}}"><input type="hidden" name="_csrf" value="{{$.Shell.CSRFToken}}"><button class="button primary" type="submit" aria-label="Join {{.Name}}">Join</button></form>{{end}}
          {{if and .IsMember .CanLeave}}<form method="post" action="/app/conversation/leave?channel={{.ID}}"><input type="hidden" name="_csrf" value="{{$.Shell.CSRFToken}}"><button class="button" type="submit" aria-label="Leave {{.Name}}">Leave</button></form>{{end}}
        </div>
      </li>{{else}}<li class="browse-empty">{{if .Query}}No channels match “{{.Query}}”. Try another search, or create the channel.{{else}}No channels to show.{{end}}</li>{{end}}</ul>
    </main>
  </div>
</div>
{{template "shell-dialogs" .Shell}}{{end}}`

var browseChannelsTemplate = mustPage(browseChannelsMarkup)

// browseChannelsLimit bounds how many channels one Browse channels page reads.
const browseChannelsLimit = 1000

func (h Handler) browseChannels(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	query := r.URL.Query()
	data := browseChannelsData{
		Query: strings.TrimSpace(query.Get("q")), Type: query.Get("type"), Sort: query.Get("sort"),
		HideMine: query.Get("hide_mine") == "1", CanJoin: principal.HasScope(auth.ScopeChannelsManage),
	}
	switch data.Type {
	case "public", "private", "archived":
	default:
		data.Type = "all"
	}
	request := domain.ConversationListRequest{
		Limit: conversationWindow, Query: data.Query, ExcludeArchived: data.Type != "archived",
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	}
	var conversations []domain.Conversation
	for {
		page, listErr := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, request)
		if listErr != nil {
			data.Error = "The channel directory is temporarily unavailable. Try again."
			break
		}
		conversations = append(conversations, page.Conversations...)
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		if len(conversations) >= browseChannelsLimit {
			data.Truncated = true
			break
		}
		request.Cursor = page.NextCursor
	}
	for _, conversation := range conversations {
		private := conversation.Kind == domain.ConversationTypePrivate
		switch {
		case conversation.IsDirectOrGroup(),
			data.Type == "public" && private,
			data.Type == "private" && !private,
			data.Type == "archived" && !conversation.Archived,
			data.HideMine && conversation.IsMember:
			continue
		}
		topic := strings.TrimSpace(conversation.Purpose)
		if topic == "" {
			topic = strings.TrimSpace(conversation.Topic)
		}
		data.Channels = append(data.Channels, conversationView{
			ID: string(conversation.ID), Name: conversationName(conversation), Kind: conversationKind(conversation),
			IsPrivate: private, IsMember: conversation.IsMember, Archived: conversation.Archived,
			MemberCount: conversation.NumMembers, Topic: topic, RecentAt: conversation.Created,
			CanLeave: conversation.IsMember && !conversation.IsGeneral && !conversation.Archived,
		})
	}
	sort.SliceStable(data.Channels, func(left, right int) bool {
		a, b := data.Channels[left], data.Channels[right]
		switch data.Sort {
		case "name-desc":
			return strings.ToLower(a.Name) > strings.ToLower(b.Name)
		case "members":
			if a.MemberCount != b.MemberCount {
				return a.MemberCount > b.MemberCount
			}
		case "members-asc":
			if a.MemberCount != b.MemberCount {
				return a.MemberCount < b.MemberCount
			}
		case "newest":
			if !a.RecentAt.Equal(b.RecentAt) {
				return a.RecentAt.After(b.RecentAt)
			}
		case "oldest":
			if !a.RecentAt.Equal(b.RecentAt) {
				return a.RecentAt.Before(b.RecentAt)
			}
		default:
			data.Sort = "name"
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	if data.Sort == "" {
		data.Sort = "name"
	}
	data.Shell = h.newShell(r, principal, shellRequest{Destination: destinationHome})
	h.writeHTML(w, browseChannelsTemplate, data, http.StatusOK, "the channel directory could not be rendered")
}
