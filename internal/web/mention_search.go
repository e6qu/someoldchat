package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A workspace larger than the directory a page carries is searched as the
// member types a mention, as Slack's is: the composer asks /app/mentions for
// the people the typed name matches, through the same barrier-aware search
// the People tab uses.

const (
	// mentionSearchLimit is how many people one mention search returns.
	mentionSearchLimit = 10
	// mentionMemberScan bounds how many of the conversation's members a
	// search reads to say who is in it; past it, who is not is left unsaid
	// rather than guessed.
	mentionMemberScan = 5000
)

type mentionPersonView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Real    string `json:"real,omitempty"`
	Display string `json:"display,omitempty"`
	Avatar  string `json:"avatar,omitempty"`
	Initial string `json:"initial"`
	Member  bool   `json:"member"`
	Unknown bool   `json:"membership_unknown,omitempty"`
	Bot     bool   `json:"bot,omitempty"`
	Self    bool   `json:"self,omitempty"`
}

func searchPeopleURL(truncated bool, conversation domain.Conversation) string {
	if !truncated {
		return ""
	}
	return "/app/mentions?" + url.Values{"channel": {string(conversation.ID)}}.Encode()
}

func (h Handler) mentionSearch(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	conversation := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	people := []mentionPersonView{}
	if query == "" || utf8.RuneCountInString(query) > 80 {
		writeMentionPeople(w, people)
		return
	}
	page, err := h.Messages.SearchPeople(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{Limit: mentionSearchLimit})
	if err != nil {
		h.writeStoreError(w, err, "Mention suggestions are temporarily unavailable.")
		return
	}
	members, complete := h.conversationMemberSet(r, principal, conversation)
	for _, user := range page.Users {
		if user.Deleted || (user.IsSlackbot() && !members[user.ID]) {
			continue
		}
		name := displayName(user)
		people = append(people, mentionPersonView{
			ID: string(user.ID), Name: name, Real: strings.TrimSpace(user.RealName), Display: strings.TrimSpace(user.Profile.DisplayName),
			Avatar: profileImageURL(user.Profile), Initial: initial(name), Member: members[user.ID], Unknown: !complete && !members[user.ID],
			Bot: user.IsBot(), Self: user.ID == principal.UserID,
		})
	}
	writeMentionPeople(w, people)
}

// conversationMemberSet reads who is in the conversation, up to
// mentionMemberScan of them; complete is false when it stopped short or
// could not read them, so nobody is called a non-member on a guess.
func (h Handler) conversationMemberSet(r *http.Request, principal auth.Principal, conversation domain.ConversationID) (map[domain.UserID]bool, bool) {
	members := map[domain.UserID]bool{}
	if conversation == "" {
		return members, false
	}
	request := domain.PageRequest{Limit: 200}
	for len(members) < mentionMemberScan {
		page, err := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, conversation, request)
		if err != nil {
			return members, false
		}
		for _, user := range page.Users {
			members[user.ID] = true
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			return members, true
		}
		request.Cursor = page.NextCursor
	}
	return members, false
}

func writeMentionPeople(w http.ResponseWriter, people []mentionPersonView) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(people)
}
