package domain

import "time"

// SlackbotUserID is Slackbot's user ID. Slack gives Slackbot the same ID in
// every workspace, and clients rely on it: an app ignores messages from
// USLACKBOT, and a client recognises the Slackbot DM by it. So it is a
// constant here too, never a generated ID.
//
// The ID is shared, but Slackbot itself is local to each workspace: its DM with
// a member, the messages it posts and the reminders it delivers all belong to
// that member's workspace. Only the stored identity row is shared, because
// every message and conversation membership references a stored user.
const SlackbotUserID UserID = "USLACKBOT"

// SlackbotHomeWorkspaceID is the reserved workspace that owns Slackbot's
// identity row. A user row needs a workspace, and Slackbot belongs to none of
// the real ones more than another. Nobody can sign in to it, and no API reads
// it: Slackbot is always presented in the caller's workspace (see
// SlackbotUser).
const SlackbotHomeWorkspaceID WorkspaceID = "TSLACKBOT"

// SlackbotUser is Slackbot as the given workspace sees it. Its profile is
// fixed, as on Slack: it cannot be renamed, deactivated or signed in to, and it
// is always present.
func SlackbotUser(workspace WorkspaceID) User {
	return User{
		ID: SlackbotUserID, WorkspaceID: workspace, Name: "slackbot", RealName: "Slackbot",
		Profile:  UserProfile{DisplayName: "Slackbot", FirstName: "slackbot"},
		Presence: PresenceAuto,
	}
}

// IsSlackbot reports whether the user is Slackbot.
func (u User) IsSlackbot() bool {
	return u.ID == SlackbotUserID
}

// SlackbotPost is a message Slackbot posts for a member: a reminder the member
// set or was given. With no Conversation it goes to the member's Slackbot DM;
// with one, it goes there, and the member must be able to post in it, so
// Slackbot never reaches a conversation the member could not.
type SlackbotPost struct {
	Conversation ConversationID
	Text         string
	// Blocks is the message's Block Kit, such as a reminder's controls; Text
	// is its fallback.
	Blocks         string
	IdempotencyKey string
}

// PresenceAt is the member's presence at now. Slackbot is always active, as on
// Slack, where its profile is always_active.
func (u User) PresenceAt(now time.Time) string {
	if u.IsSlackbot() {
		return "active"
	}
	return u.Presence.CurrentAt(u.LastActiveAt, now)
}
