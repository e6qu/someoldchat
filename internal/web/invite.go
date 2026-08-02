package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// The invitation page is the only surface in this product a signed-out visitor
// reaches on purpose, and it must be readable without an account: the person it
// is for does not have one yet. That is why it carries no secret.
//
// Possessing the link grants nothing. Acceptance is decided against an address
// the identity provider has verified (see LoginHandler.resolveIdentityUser and
// service.Messages.AcceptInvitationForEmail), so the link only tells someone
// where to sign in. The invited address is masked all the same: an invitation
// identifier travels in the durable event journal, so it is workspace-visible,
// and an address is not something to hand out to whoever holds one.

type invitePageData struct {
	Title string
	// Lead is the one sentence that says what this page is about. Outcome and
	// Detail then say where the invitation stands and what to do next; every
	// terminal state gets its own wording, because "this did not work" leaves
	// the reader unable to tell whether to wait, retry or ask for a new one.
	Lead     string
	Outcome  string
	Detail   string
	Address  string
	Tier     string
	Channels []string
	Expires  string
	SignInAt string
}

const inviteMarkup = `{{define "title"}}{{.Title}} · SameOldChat{{end}}
{{define "styles"}}<style>
body{min-height:100vh;display:grid;place-items:center;padding:24px}
.invite{width:min(520px,100%);padding:32px;background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:var(--shadow)}
.invite h1{margin:0 0 10px;font-size:1.9rem}
.invite p{margin:0 0 16px}
.invite .lead{color:var(--muted)}
.invite dl{display:grid;grid-template-columns:auto minmax(0,1fr);gap:6px 14px;margin:0 0 20px}
.invite dt{color:var(--muted);font-weight:700}
.invite dd{margin:0;overflow-wrap:anywhere}
.invite .accept{display:inline-block;padding:11px 16px;border-radius:7px;background:var(--accent);color:var(--on-accent);font-weight:800;text-decoration:none}
.invite .outcome{padding:12px 14px;border:1px solid var(--line);border-radius:8px;background:var(--bg)}
</style>{{end}}
{{define "content"}}<main class="invite">
<h1>{{.Title}}</h1>
<p class="lead">{{.Lead}}</p>
{{if .Address}}<dl>
<dt>Invitation for</dt><dd>{{.Address}}</dd>
<dt>Access</dt><dd>{{.Tier}}</dd>
{{if .Channels}}<dt>Channels</dt><dd>{{range .Channels}}#{{.}} {{end}}</dd>{{end}}
{{if .Expires}}<dt>Valid until</dt><dd>{{.Expires}}</dd>{{end}}
</dl>{{end}}
{{if .Outcome}}<p class="outcome" role="status">{{.Outcome}}</p>{{end}}
{{if .Detail}}<p>{{.Detail}}</p>{{end}}
{{if .SignInAt}}<p><a class="accept" href="{{.SignInAt}}">Sign in to accept</a></p>{{end}}
</main>{{end}}`

var inviteTemplate = mustPage(inviteMarkup)

// maskEmailAddress keeps enough of an address for the invited person to
// recognise their own and not enough for anyone else to learn it.
func maskEmailAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at <= 0 {
		return "•••"
	}
	local, domainPart := address[:at], address[at+1:]
	visible := 1
	if len(local) < 2 {
		visible = 0
	}
	return local[:visible] + strings.Repeat("•", 3) + "@" + domainPart
}

func inviteTierLabel(request domain.InviteRequest) string {
	switch {
	case request.UltraRestricted:
		return "Guest in one channel"
	case request.Restricted:
		return "Guest in several channels"
	default:
		return "Full workspace member"
	}
}

func (h Handler) invitationPage(w http.ResponseWriter, r *http.Request) {
	if h.Login == nil {
		http.NotFound(w, r)
		return
	}
	id := domain.InviteRequestID(strings.TrimSpace(r.PathValue("inviteRequestID")))
	target := "/app/invite/" + url.PathEscape(string(id))
	request, err := h.Messages.InvitationPreview(r.Context(), h.Login.workspace, id)
	if errors.Is(err, store.ErrNotFound) {
		h.writeInvitePage(w, http.StatusNotFound, invitePageData{
			Title:   "This invitation does not exist",
			Lead:    "The link may have been mistyped, or the invitation may have been removed.",
			Outcome: "Nothing here can be accepted.",
			Detail:  "Ask whoever invited you to send a new invitation.",
		})
		return
	}
	if err != nil {
		h.writeInvitePage(w, http.StatusServiceUnavailable, invitePageData{
			Title:   "The invitation could not be read",
			Lead:    "This is a temporary problem with the workspace, not with your invitation.",
			Outcome: "Nothing was accepted.",
			Detail:  "Try the link again in a few minutes.",
		})
		return
	}

	data := invitePageData{
		Address: maskEmailAddress(request.Email),
		Tier:    inviteTierLabel(request),
	}
	if !request.ExpiresAt.IsZero() {
		data.Expires = formatTime(request.ExpiresAt)
	}
	// The channel names are read with the configured lookup identity — a plain
	// member — because the visitor has none. A channel it cannot see renders as
	// "private channel" rather than leaking a name.
	names := h.newUserNames(r.Context(), auth.Principal{WorkspaceID: h.Login.workspace, UserID: h.Login.lookupUser})
	for _, channelID := range request.ChannelIDs {
		data.Channels = append(data.Channels, names.channelName(channelID))
	}

	status := http.StatusOK
	switch {
	case request.Status == domain.InviteRequestAccepted:
		status = http.StatusGone
		data.Title = "This invitation has already been accepted"
		data.Lead = "An invitation can be accepted once."
		data.Outcome = "Someone has already joined the workspace with it."
		data.Detail = "If that was you, sign in as usual. If it was not, tell a workspace administrator."
	case request.Status == domain.InviteRequestRevoked || request.Status == domain.InviteRequestDenied:
		status = http.StatusGone
		data.Title = "This invitation was withdrawn"
		data.Lead = "A workspace administrator withdrew it before it was accepted."
		data.Outcome = "It can no longer be accepted."
		data.Detail = "Ask them to send a new invitation if you still need access."
	case request.Status == domain.InviteRequestPending:
		data.Title = "This invitation is waiting for approval"
		data.Lead = "It has been recorded, and a workspace administrator has not yet approved it."
		data.Outcome = "It cannot be accepted yet."
		data.Detail = "Come back to this link once it has been approved."
	case !request.Acceptable(time.Now().UTC()):
		status = http.StatusGone
		data.Title = "This invitation has expired"
		data.Lead = "An invitation is valid for a limited time after it is recorded."
		data.Outcome = "It can no longer be accepted."
		data.Detail = "Ask whoever invited you to send a new one."
	default:
		data.Title = "You have been invited to SameOldChat"
		data.Lead = "Sign in with the address below and the account is created for you, with the access and channels recorded here."
		data.SignInAt = "/login?return_to=" + url.QueryEscape(target)
	}
	h.writeInvitePage(w, status, data)
}

func (h Handler) writeInvitePage(w http.ResponseWriter, status int, data invitePageData) {
	h.writeHTMLWithPolicy(w, inviteTemplate, data, status, "the invitation could not be rendered", inviteContentSecurityPolicy)
}

// inviteContentSecurityPolicy is the entry policy plus the hashes of the two
// scripts the shared layout carries. The page is reached signed-out and posts
// nothing, so form-action stays 'none'.
var inviteContentSecurityPolicy = "default-src 'none'; script-src " +
	strings.Join(inlineScriptHashes(layoutMarkup, inviteMarkup), " ") +
	"; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
