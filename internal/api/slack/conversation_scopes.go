package slack

import (
	"net/http"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// conversationGrant is Slack's per-conversation-type scope model for one kind
// of access. Reading a public channel's history takes channels:history, a
// private channel's groups:history, a direct message's im:history and a group
// direct message's mpim:history; metadata and writes follow the same shape.
// The pinned snapshot lists all four for each conversations.* method (its
// `security` requirement), and Slack's method references assign them per
// conversation type.
//
// This handler used to check one scope — channels:history for every history
// read, channels:read for every metadata read, channels:manage for every
// write — so a token granted only channels:history read private channels and
// DMs, and a token correctly granted im:history could read no DM at all.
//
// A grant is enforced in two steps. authenticateConversation refuses a token
// that holds none of the family before anything is read, answering
// missing_scope with every scope that would do, which is Slack's answer too.
// Once the conversation is known, authorizeConversation requires the one scope
// its type needs.
type conversationGrant struct {
	public auth.Scope
	// publicBot, when set, replaces public for bot tokens: Slack's granular
	// bot scopes manage public channels with channels:manage where a user
	// token uses channels:write.
	publicBot auth.Scope
	private   auth.Scope
	im        auth.Scope
	mpim      auth.Scope
}

var (
	conversationHistoryGrant = conversationGrant{
		public: auth.ScopeChannelsHistory, private: auth.ScopeGroupsHistory,
		im: auth.ScopeIMHistory, mpim: auth.ScopeMPIMHistory,
	}
	conversationReadGrant = conversationGrant{
		public: auth.ScopeChannelsRead, private: auth.ScopeGroupsRead,
		im: auth.ScopeIMRead, mpim: auth.ScopeMPIMRead,
	}
	conversationWriteGrant = conversationGrant{
		public: auth.ScopeChannelsWrite, publicBot: auth.ScopeChannelsManage, private: auth.ScopeGroupsWrite,
		im: auth.ScopeIMWrite, mpim: auth.ScopeMPIMWrite,
	}
)

// conversationTypes is every conversation type, in the order the pinned
// snapshot lists their scopes.
var conversationTypes = []domain.ConversationType{
	domain.ConversationTypePublic, domain.ConversationTypePrivate, domain.ConversationTypeIM, domain.ConversationTypeMPIM,
}

func isBotPrincipal(principal auth.Principal) bool {
	return principal.TokenType.IsBot() || principal.BotID != ""
}

// scope is the one scope this grant requires for a conversation of kind.
func (g conversationGrant) scope(principal auth.Principal, kind domain.ConversationType) auth.Scope {
	switch kind.OrPublic() {
	case domain.ConversationTypePrivate:
		return g.private
	case domain.ConversationTypeIM:
		return g.im
	case domain.ConversationTypeMPIM:
		return g.mpim
	default:
		if g.publicBot != "" && isBotPrincipal(principal) {
			return g.publicBot
		}
		return g.public
	}
}

// scopes is the grant's scope for each of kinds, or for every type when kinds
// is empty, always in the snapshot's channels, groups, im, mpim order so a
// `needed` value does not depend on how the caller spelled its types.
func (g conversationGrant) scopes(principal auth.Principal, kinds ...domain.ConversationType) []auth.Scope {
	wanted := make(map[domain.ConversationType]struct{}, len(kinds))
	for _, kind := range kinds {
		wanted[kind.OrPublic()] = struct{}{}
	}
	values := make([]auth.Scope, 0, len(conversationTypes))
	seen := make(map[auth.Scope]struct{}, len(conversationTypes))
	for _, kind := range conversationTypes {
		if _, ok := wanted[kind]; len(wanted) != 0 && !ok {
			continue
		}
		scope := g.scope(principal, kind)
		if _, duplicate := seen[scope]; duplicate {
			continue
		}
		seen[scope] = struct{}{}
		values = append(values, scope)
	}
	return values
}

// permits reports whether the principal may reach a conversation of kind.
func (g conversationGrant) permits(principal auth.Principal, kind domain.ConversationType) bool {
	return principal.HasScope(g.scope(principal, kind))
}

// authenticateConversation authenticates the request and refuses a token that
// holds none of the grant's scopes for kinds (every type when none is named).
func (h Handler) authenticateConversation(r *http.Request, grant conversationGrant, kinds ...domain.ConversationType) (auth.Principal, error) {
	principal, err := h.authenticate(r, "")
	if err != nil {
		return auth.Principal{}, err
	}
	if err := requireAnyScope(r, principal, grant.scopes(principal, kinds...)...); err != nil {
		return auth.Principal{}, err
	}
	return principal, nil
}

// authorizeConversation requires the one scope a conversation's type needs
// under grant.
func authorizeConversation(r *http.Request, principal auth.Principal, grant conversationGrant, conversation domain.Conversation) error {
	return requireAnyScope(r, principal, grant.scope(principal, conversation.Kind))
}

// authorizedConversation reads the conversation a request names and requires
// the scope its type needs under grant, answering the failure itself: the
// caller's own not-found code when the conversation cannot be read, and
// missing_scope when its type is out of the token's reach. It reports whether
// the caller may proceed.
func (h Handler) authorizedConversation(w http.ResponseWriter, r *http.Request, principal auth.Principal, grant conversationGrant, channel domain.ConversationID, notFound string) (domain.Conversation, bool) {
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		writeError(w, mapServiceError(err, notFound))
		return domain.Conversation{}, false
	}
	if err := authorizeConversation(r, principal, grant, conversation); err != nil {
		writeAuthError(w, err)
		return domain.Conversation{}, false
	}
	return conversation, true
}
