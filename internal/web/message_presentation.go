package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
)

// timezoneCookie carries the reader's IANA time zone, which the page script
// writes from the browser. The timeline's day dividers and clock times are
// server-rendered — the live refresh re-renders them — so the server has to
// know which calendar day a message fell on for this reader, not in UTC.
const timezoneCookie = "sameoldchat_tz"

// readerLocation is the reader's time zone, or UTC until the browser has
// reported one. An unknown or malformed zone is UTC rather than an error: it
// only changes where a divider falls.
func readerLocation(r *http.Request) *time.Location {
	cookie, err := r.Cookie(timezoneCookie)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 64 {
		return time.UTC
	}
	location, err := time.LoadLocation(cookie.Value)
	if err != nil {
		return time.UTC
	}
	return location
}

// continuationWindow is how long after a member's message their next one is
// still shown as part of the same group, without repeating name and avatar.
const continuationWindow = 5 * time.Minute

// clockTime is the time Slack prints beside a name: "10:19 AM".
func clockTime(value time.Time, location *time.Location) string {
	return value.In(location).Format("3:04 PM")
}

// fullTime is the timestamp tooltip: "Saturday, September 27th at 10:19:05 AM".
func fullTime(value time.Time, location *time.Location) string {
	local := value.In(location)
	return local.Format("Monday, January ") + ordinal(local.Day()) + " at " + local.Format("3:04:05 PM")
}

// dayLabel names a day divider the way Slack does: "Today", "Yesterday", or
// "Friday, September 26th", with the year only when it is not this one.
func dayLabel(value, now time.Time, location *time.Location) string {
	local := value.In(location)
	today := now.In(location)
	year, month, day := local.Date()
	if todayYear, todayMonth, todayDay := today.Date(); year == todayYear && month == todayMonth && day == todayDay {
		return "Today"
	}
	if yesterdayYear, yesterdayMonth, yesterdayDay := today.AddDate(0, 0, -1).Date(); year == yesterdayYear && month == yesterdayMonth && day == yesterdayDay {
		return "Yesterday"
	}
	label := local.Format("Monday, January ") + ordinal(day)
	if year != today.Year() {
		label += ", " + strconv.Itoa(year)
	}
	return label
}

func ordinal(day int) string {
	suffix := "th"
	if day%100 < 11 || day%100 > 13 {
		switch day % 10 {
		case 1:
			suffix = "st"
		case 2:
			suffix = "nd"
		case 3:
			suffix = "rd"
		}
	}
	return strconv.Itoa(day) + suffix
}

// relativeTime is the thread summary's "5 minutes ago". The page script
// rewrites it in the reader's locale; this is the server's first paint.
func relativeTime(value, now time.Time) string {
	elapsed := now.Sub(value)
	plural := func(count int, unit string) string {
		if count == 1 {
			return "1 " + unit + " ago"
		}
		return strconv.Itoa(count) + " " + unit + "s ago"
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return plural(int(elapsed/time.Minute), "minute")
	case elapsed < 24*time.Hour:
		return plural(int(elapsed/time.Hour), "hour")
	case elapsed < 30*24*time.Hour:
		return plural(int(elapsed/(24*time.Hour)), "day")
	case elapsed < 365*24*time.Hour:
		return plural(int(elapsed/(30*24*time.Hour)), "month")
	}
	return plural(int(elapsed/(365*24*time.Hour)), "year")
}

// replyCountLabel is "1 reply" or "N replies".
func replyCountLabel(count int) string {
	if count == 1 {
		return "1 reply"
	}
	return strconv.Itoa(count) + " replies"
}

// startsGroup reports whether a message begins a new visual group rather than
// continuing the one above it. Slack repeats the author's name and avatar
// when the author changes, when more than a few minutes have passed, across a
// day divider, the unread line or the thread pane's reply divider, and
// around workspace notices, broadcasts
// and messages wearing a custom app name or icon.
func startsGroup(previous, current messageView, previousAt, currentAt time.Time) bool {
	if current.DaySeparator != "" || current.FirstUnread || current.System || previous.System || previous.ThreadRoot {
		return true
	}
	if current.Broadcast || previous.Broadcast || current.Ephemeral != previous.Ephemeral {
		return true
	}
	if current.AuthorID == "" || current.AuthorID != previous.AuthorID || current.AuthorName != previous.AuthorName || current.AvatarURL != previous.AvatarURL || current.AvatarEmoji != previous.AvatarEmoji {
		return true
	}
	return currentAt.Sub(previousAt) > continuationWindow || currentAt.Before(previousAt)
}

// systemSentence is what Slack's client prints after the author's name for a
// channel notice: "joined #general.", "set the channel topic: …". The stored
// text keeps the Web API sentence; this is its display form.
func systemSentence(message domain.Message, channelLabel string) (string, bool) {
	fields := domain.ChannelNoticeFields(message)
	switch message.Subtype {
	case domain.MessageSubtypeChannelJoin:
		if channelLabel == "" {
			return "joined.", true
		}
		return "joined " + channelLabel + ".", true
	case domain.MessageSubtypeChannelLeave:
		if channelLabel == "" {
			return "left.", true
		}
		return "left " + channelLabel + ".", true
	case domain.MessageSubtypeChannelTopic:
		if fields == nil {
			return "", false
		}
		if fields["topic"] == "" {
			return "cleared the channel topic", true
		}
		return "set the channel topic: " + fields["topic"], true
	case domain.MessageSubtypeChannelPurpose:
		if fields == nil {
			return "", false
		}
		if fields["purpose"] == "" {
			return "cleared the channel description", true
		}
		return "set the channel description: " + fields["purpose"], true
	case domain.MessageSubtypeChannelName:
		if fields == nil || fields["name"] == "" {
			return "", false
		}
		if fields["old_name"] == "" {
			return "renamed the channel to #" + fields["name"], true
		}
		return "renamed the channel from #" + fields["old_name"] + " to #" + fields["name"], true
	}
	return "", false
}

// plainPreview is a one-line, markup-free excerpt of a message — the root
// preview beside a thread broadcast. References are shown by their label and
// formatting characters are dropped, so it reads as the message does.
func plainPreview(text string, limit int) string {
	text = decodeSlackEntities(text)
	var output strings.Builder
	for offset := 0; offset < len(text); {
		switch character := text[offset]; character {
		case '<':
			end := strings.IndexByte(text[offset+1:], '>')
			if end < 0 {
				output.WriteByte(character)
				offset++
				continue
			}
			raw := text[offset+1 : offset+1+end]
			target, label, _ := strings.Cut(raw, "|")
			if label == "" {
				label = target
				if strings.HasPrefix(target, "!") {
					label = slackSpecialReferenceLabel(target)
				}
			}
			output.WriteString(label)
			offset += end + 2
		case '*', '_', '~', '`', '\n':
			if character == '\n' {
				output.WriteByte(' ')
			}
			offset++
		default:
			output.WriteByte(character)
			offset++
		}
	}
	preview := strings.Join(strings.Fields(output.String()), " ")
	if utf8.RuneCountInString(preview) > limit {
		runes := []rune(preview)
		preview = strings.TrimSpace(string(runes[:limit])) + "…"
	}
	return preview
}

// reactionTooltip is Slack's hover text on a reaction pill: "Ada, Grace and
// you reacted with :eyes:". Names beyond the first few are counted.
func reactionTooltip(reactors []string, name string) string {
	const shown = 5
	people := reactors
	others := 0
	if len(people) > shown {
		others = len(people) - shown
		people = people[:shown]
	}
	var who string
	switch {
	case others > 0:
		who = strings.Join(people, ", ") + " and " + strconv.Itoa(others) + " other"
		if others > 1 {
			who += "s"
		}
	case len(people) == 1:
		who = people[0]
	default:
		who = strings.Join(people[:len(people)-1], ", ") + " and " + people[len(people)-1]
	}
	if strings.HasPrefix(who, "you") {
		who = "You" + who[len("you"):]
	}
	return who + " reacted with :" + emojiDisplayName(strings.Trim(name, ":")) + ":"
}

// messageDialogView is a message-level confirmation rendered open by the
// server: Delete message or Forward message for the named message.
type messageDialogView struct {
	Kind    string
	Message messageView
	// CloseURL returns to the conversation with the dialog dismissed.
	CloseURL string
}

// requestedMessageDialog answers the ?delete= and ?forward= links a reader
// without script follows from a message menu. The message must be one the
// page rendered and, for deletion, one the reader may delete; anything else
// renders no dialog rather than an error, because the link is only a way of
// opening a form the reader could already reach.
func requestedMessageDialog(r *http.Request, lists ...messageList) *messageDialogView {
	query := r.URL.Query()
	kind, timestamp := "delete", strings.TrimSpace(query.Get("delete"))
	if timestamp == "" {
		kind, timestamp = "forward", strings.TrimSpace(query.Get("forward"))
	}
	if timestamp == "" {
		return nil
	}
	for _, list := range lists {
		for _, message := range list.Messages {
			if message.Timestamp != timestamp || message.Ephemeral {
				continue
			}
			if (kind == "delete" && !message.CanDelete) || (kind == "forward" && message.ForwardURL == "") {
				return nil
			}
			closeQuery := r.URL.Query()
			closeQuery.Del("delete")
			closeQuery.Del("forward")
			return &messageDialogView{Kind: kind, Message: message, CloseURL: "/app?" + closeQuery.Encode() + "#" + message.Anchor}
		}
	}
	return nil
}

// replierView is one avatar in a thread summary row.
type replierView struct {
	Name    string
	Initial string
}

// quickReactionDefaults are the one-click reactions a member sees in the
// message toolbar before they have used any emoji: Slack's own defaults.
var quickReactionDefaults = []string{"white_check_mark", "eyes", "raised_hands"}

// recentEmojiCookie carries the three emoji this browser used most recently,
// which lead the toolbar's one-click reactions the way Slack's do. The picker
// writes it; the server only reads names it can render.
const recentEmojiCookie = "sameoldchat_recent_emoji"

// recentReactionNames reads recentEmojiCookie: at most three dot-separated,
// URL-escaped colon-code names.
func recentReactionNames(r *http.Request) []string {
	cookie, err := r.Cookie(recentEmojiCookie)
	if err != nil || cookie.Value == "" || len(cookie.Value) > 400 {
		return nil
	}
	names := make([]string, 0, 3)
	for _, raw := range strings.Split(cookie.Value, ".") {
		name, err := url.QueryUnescape(raw)
		if err != nil || !validEmojiCode(name) {
			continue
		}
		names = append(names, strings.ToLower(name))
		if len(names) == 3 {
			break
		}
	}
	return names
}

type quickReactionView struct {
	Name    string
	Label   string
	Display template.HTML
}

// quickReactions is the toolbar's one-click reactions: the reader's recent
// emoji first, then Slack's defaults, three in all. A recent name that is no
// longer an emoji — a deleted custom one — is skipped rather than offered.
func quickReactions(recent []string, customEmoji map[string]string) []quickReactionView {
	views := make([]quickReactionView, 0, len(quickReactionDefaults))
	seen := map[string]bool{}
	for _, name := range append(append([]string(nil), recent...), quickReactionDefaults...) {
		if len(views) == len(quickReactionDefaults) || seen[name] {
			continue
		}
		if _, standard := slackemoji.Lookup(name); !standard && customEmoji[name] == "" {
			continue
		}
		seen[name] = true
		views = append(views, quickReactionView{Name: name, Label: emojiDisplayName(name), Display: renderReactionEmoji(name, customEmoji)})
	}
	return views
}

// setMutationNotice attaches the confirmation a completed in-place mutation
// shows as a toast. It is percent-encoded because a header carries Latin-1 at
// best and a channel name need not be; the page script decodes it.
func setMutationNotice(w http.ResponseWriter, notice string) {
	w.Header().Set("X-SameOldChat-Notice", url.PathEscape(notice))
}

// emojiOptionsLimit bounds one emoji options answer. The largest standard
// category, People & Body, has under four hundred emoji; the bound leaves room
// for a workspace's custom set without letting one request serialise an
// unbounded list.
const emojiOptionsLimit = 2000

// reminderConfirmation is the toast Slack shows after "Remind me about this":
// "Got it! We’ll remind you about this message at 3:15 PM.", with the day
// named when it is not today.
func reminderConfirmation(due time.Time, zone string, now time.Time) string {
	location, err := time.LoadLocation(zone)
	if err != nil {
		location = time.UTC
	}
	local, today := due.In(location), now.In(location)
	when := "at " + local.Format("3:04 PM")
	switch label := dayLabel(due, now, location); {
	case label == "Today":
	case label == "Yesterday":
		when = local.Format("Monday") + " " + when
	case local.Sub(today) < 48*time.Hour && local.YearDay() != today.YearDay():
		when = "tomorrow " + when
	default:
		when = "on " + label + " " + when
	}
	return "Got it! We'll remind you about this message " + when + "."
}

// emojiDisplayName is the name a person is shown for an emoji. The catalog's
// canonical names for the thumbs are "+1" and "-1"; Slack's picker and
// tooltips call them :thumbsup: and :thumbsdown:.
func emojiDisplayName(name string) string {
	switch name {
	case "+1":
		return "thumbsup"
	case "-1":
		return "thumbsdown"
	}
	return name
}

type emojiSkinToneView struct {
	Value string
	Label string
	Glyph string
}

// emojiSkinTones are the picker's skin-tone choices, "" being the default.
func emojiSkinTones() []emojiSkinToneView {
	return []emojiSkinToneView{
		{Value: "", Label: "Default skin tone", Glyph: "✋"},
		{Value: "2", Label: "Light skin tone", Glyph: "✋\U0001F3FB"},
		{Value: "3", Label: "Medium-light skin tone", Glyph: "✋\U0001F3FC"},
		{Value: "4", Label: "Medium skin tone", Glyph: "✋\U0001F3FD"},
		{Value: "5", Label: "Medium-dark skin tone", Glyph: "✋\U0001F3FE"},
		{Value: "6", Label: "Dark skin tone", Glyph: "✋\U0001F3FF"},
	}
}

type emojiCategoryTab struct {
	Name  string
	Label string
	Glyph string
}

// emojiCategoryTabs is the picker's category row in Slack's order, frequently
// used first and the workspace's own emoji last. Name is the category the
// emoji options endpoint filters by.
func emojiCategoryTabs() []emojiCategoryTab {
	return []emojiCategoryTab{
		{Name: "Recent", Label: "Frequently used", Glyph: "🕘"},
		{Name: "Smileys & Emotion", Label: "Smileys & emotion", Glyph: "😀"},
		{Name: "People & Body", Label: "People & body", Glyph: "👋"},
		{Name: "Animals & Nature", Label: "Animals & nature", Glyph: "🐻"},
		{Name: "Food & Drink", Label: "Food & drink", Glyph: "🍔"},
		{Name: "Activities", Label: "Activities", Glyph: "⚽"},
		{Name: "Travel & Places", Label: "Travel & places", Glyph: "🚗"},
		{Name: "Objects", Label: "Objects", Glyph: "💡"},
		{Name: "Symbols", Label: "Symbols", Glyph: "💟"},
		{Name: "Flags", Label: "Flags", Glyph: "🏁"},
		{Name: "Custom", Label: "Custom", Glyph: "✨"},
	}
}
