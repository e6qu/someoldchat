package domain

// UnfurlAuthPrompt is chat.unfurl's invitation to the member who shared a link
// to connect their account to the app, so the app can unfurl it fully. Slack
// sends it as an ephemeral message carrying the app's text or blocks and two
// buttons of its own, "Not now" and "Never ask me again".
type UnfurlAuthPrompt struct {
	// Message is user_auth_message, the invitation's text. It takes
	// precedence over the default text when both it and URL are given.
	Message string
	// URL is user_auth_url, where the member completes authentication.
	URL string
	// Blocks is user_auth_blocks, normalized, replacing the default content.
	Blocks string
}

// The two buttons Slack adds to every unfurl authentication prompt. The
// product answers them itself; they never reach the app.
const (
	UnfurlAuthBlockID        = "sameoldchat_unfurl_auth"
	UnfurlAuthNotNowAction   = "sameoldchat_unfurl_auth_not_now"
	UnfurlAuthNeverAskAction = "sameoldchat_unfurl_auth_never_ask"
)
