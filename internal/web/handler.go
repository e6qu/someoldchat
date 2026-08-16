package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/slackemoji"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type Handler struct {
	Messages        chatapi.Service
	Authenticator   auth.Authenticator
	SessionRevoker  auth.SessionRevoker
	Channel         domain.ConversationID
	CookieDomain    string
	Login           *LoginHandler
	PublicURL       string
	ReleaseRevision string
}

var immutableReleaseRevision = regexp.MustCompile(`^[0-9a-f]{12,64}$|^sha256:[0-9a-f]{64}$`)
var immutableCommitRevision = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
var remindInPattern = regexp.MustCompile(`(?i)^(.*?)\s+in\s+(a|an|[1-9][0-9]*)\s+(minute|minutes|hour|hours|day|days|week|weeks|month|months|year|years)$`)
var remindTomorrowPattern = regexp.MustCompile(`(?i)^(.*?)\s+tomorrow(?:\s+at\s+(.+))?$`)
var remindDatePattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+([0-9]{4}-[0-9]{2}-[0-9]{2})(?:\s+at\s+(.+))?$`)
var remindWeekdayPattern = regexp.MustCompile(`(?i)^(.*?)\s+every\s+(sunday|monday|tuesday|wednesday|thursday|friday|saturday)(?:\s+at\s+(.+))?$`)
var remindRecurringPattern = regexp.MustCompile(`(?i)^(.*?)\s+every\s+(day|week|month|year)(?:\s+at\s+(.+))?$`)
var remindOnWeekdayPattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+(sunday|monday|tuesday|wednesday|thursday|friday|saturday)(?:\s+at\s+(.+))?$`)
var remindOnMonthDayPattern = regexp.MustCompile(`(?i)^(.*?)\s+on\s+(january|february|march|april|may|june|july|august|september|october|november|december|jan|feb|mar|apr|jun|jul|aug|sept?|oct|nov|dec)\s+([0-9]{1,2})(?:\s+at\s+(.+))?$`)
var remindTodayPattern = regexp.MustCompile(`(?i)^(.*?)\s+at\s+(.+)$`)

// ValidateReleaseRevision decides, without constructing anything, whether a
// release identity is acceptable. It is exported so a process can settle the
// question inside its configuration contract — cmd/server's -check-config
// accepted `-release-revision not-a-commit` and the real start then exited 2 on
// it, which is precisely the drift scripts/check-terraform-module-startup.sh
// treats -check-config as authoritative against.
func ValidateReleaseRevision(revision string) error {
	if !immutableReleaseRevision.MatchString(strings.TrimSpace(revision)) {
		return errors.New("web release revision must be an immutable commit or image digest")
	}
	return nil
}

func (h *Handler) SetReleaseRevision(revision string) error {
	if err := ValidateReleaseRevision(revision); err != nil {
		return err
	}
	revision = strings.TrimSpace(revision)
	if immutableCommitRevision.MatchString(revision) {
		revision = revision[:12]
	}
	h.ReleaseRevision = revision
	return nil
}

func ValidatePublicURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !validAuthorizationURL(parsed) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("web public URL must be an absolute HTTPS URL, except for an explicit loopback development coordinate")
	}
	return nil
}

func (h *Handler) SetPublicURL(value string) error {
	if err := ValidatePublicURL(value); err != nil {
		return err
	}
	parsed, _ := url.Parse(strings.TrimSpace(value))
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	h.PublicURL = parsed.String()
	return nil
}

func NewHandler(messages chatapi.Service, authenticator auth.Authenticator, sessionRevoker auth.SessionRevoker, channel, cookieDomain string) (Handler, error) {
	if messages == nil {
		return Handler{}, errors.New("web requires a chat service")
	}
	if authenticator == nil {
		return Handler{}, errors.New("web requires an authenticator")
	}
	if sessionRevoker == nil {
		return Handler{}, errors.New("web requires a session revoker")
	}
	if channel == "" {
		return Handler{}, errors.New("web requires a channel")
	}
	if err := auth.ValidateSessionCookieDomain(cookieDomain); err != nil {
		return Handler{}, err
	}
	return Handler{Messages: messages, Authenticator: authenticator, SessionRevoker: sessionRevoker, Channel: domain.ConversationID(channel), CookieDomain: strings.TrimSpace(cookieDomain)}, nil
}

const (
	// timelineWindow is how many messages one timeline view renders.
	timelineWindow = 50
	// conversationWindow bounds the sidebar; the remainder is reachable through
	// the sidebar pager.
	conversationWindow = 50
	// directNameWindow bounds how many direct conversations resolve participant
	// names, so a large sidebar cannot turn into an unbounded member lookup.
	directNameWindow   = 20
	searchWindow       = 25
	recentSearchWindow = 10
	memberWindow       = 100
	scheduledWindow    = 50
	// reactionWindow bounds the reactions rendered for one message.
	reactionWindow = 50
	// pinWindow bounds the pins loaded for one conversation view.
	pinWindow = 100
)

// liveEventTopics are the durable event topics the workspace page reacts to.
// They have to match the topics the chat service actually publishes
// (internal/service/messages.go): an edit is published as "message.changed",
// and no component publishes "message.updated".
var liveEventTopics = []string{
	"message.created",
	"message.changed",
	"message.deleted",
	"message.unfurled",
	"reaction.added",
	"reaction.removed",
	"conversation.members_invited",
	"pin.added",
	"pin.removed",
	"saved_item.created",
	"saved_item.changed",
	"saved_item.removed",
	"view.opened",
	"view.pushed",
	"view.updated",
	"view.submitted",
	"view.closed",
	"huddle.started",
	"huddle.joined",
	"huddle.left",
	"huddle.ended",
}

// ---------------------------------------------------------------------------
// View models
// ---------------------------------------------------------------------------

// messageList is the only type the "messages" partial is ever invoked with. The
// main timeline, the thread pane and both mutation fragments render through it,
// so the partial cannot be reached with a value that is missing a field it
// needs: that mismatch is what made every thread view fail to render.
type messageList struct {
	// ForwardDestinations are the conversations this reader may forward into.
	// They are the reader's own visible channels: ACT-03 requires a forward
	// not to disclose a destination the actor cannot post to.
	ForwardDestinations []conversationView
	Messages            []messageView
	ChannelName         string
	CSRFToken           string
	IsMember            bool
	CanReact            bool
	CanPin              bool
	CanReply            bool
}

type messageView struct {
	ID            string
	MessageID     string
	Anchor        string
	AuthorName    string
	AuthorInitial string
	AvatarURL     string
	AvatarEmoji   string
	// AuthorStatus is the author's current status emoji, resolved to a glyph,
	// projected beside their name the way Slack shows it on every message. It is
	// set only for a human author posting as themselves — an app message or one
	// carrying a custom username is not a person and has no status to show.
	// AuthorStatusText is the accompanying prose, shown as the emoji's tooltip.
	AuthorStatus     template.HTML
	AuthorStatusText string
	IsApp            bool
	Text             string
	DisplayText      template.HTML
	Blocks           []messageBlockView
	Attachments      []messageAttachmentView
	Unfurls          []messageAttachmentView
	MachineTime      string
	DisplayTime      string
	Pinned           bool
	Saved            bool
	SavedItemID      string
	Reactions        []reactionView
	ReplyURL         string
	ReactionURL      string
	UnreactURL       string
	PinURL           string
	UnpinURL         string
	SaveURL          string
	UnsaveURL        string
	RemindURL        string
	UpdateURL        string
	DeleteURL        string
	CanEdit          bool
	CanDelete        bool
	Permalink        string
	// Edited, ReplyCount and their neighbours are the fittings Slack shows
	// around a message: whether it was edited, what its thread has
	// accumulated, whether it was also sent to the channel, and whether it is
	// a message the workspace generated rather than a person composing one.
	Edited            bool
	EditedTime        string
	EditedMachineTime string
	ReplyCount        int
	ReplySummary      string
	LastReplyTime     string
	Broadcast         bool
	System            bool
	Subtype           string
	// DaySeparator is set on the first message of each day in the rendered
	// window, and FirstUnread on the first message the reader has not read.
	// Both are computed server-side: the fragment refresh re-renders this
	// same partial, so anything computed in the browser is lost on every
	// live update.
	DaySeparator        string
	DaySeparatorMachine string
	FirstUnread         bool
	CopyLinkURL         string
	ForwardURL          string
	MarkUnreadURL       string
	Channel             string
	ChannelName         string
	ChannelPrefix       string
	AppID               string
	CanInteract         bool
	Streaming           bool
	Ephemeral           bool
	Shortcuts           []domain.AppShortcut
	Files               []fileView
}

type fileView struct {
	ID          string
	Name        string
	Title       string
	MIMEType    string
	Size        string
	DownloadURL string
	Deleted     bool
	// IsImage decides whether this file is shown or linked. Description is the
	// uploader's account of it, and AccessibleName is what the alt attribute
	// carries — empty when nobody has described the image and it has no title,
	// because an alt text that repeats the file name tells a screen-reader user
	// nothing while stopping them skipping the image.
	IsImage        bool
	Description    string
	AccessibleName string
	// DescribeURL is set only for an image this reader may describe, which is
	// the uploader, matching deletion.
	DescribeURL string
	// DeleteURL is set only for a file this reader may actually delete. The
	// service is uploader-only, so rendering the control for anyone else
	// would be a button that always fails — the universal contract forbids a
	// control that does not do what its name promises.
	DeleteURL string
}

type reactionView struct {
	Name    string
	Display template.HTML
	Count   int
	Mine    bool
}

type emojiOptionView struct {
	Name      string `json:"name"`
	Display   string `json:"display"`
	ImageURL  string `json:"image_url,omitempty"`
	Category  string `json:"category"`
	Custom    bool   `json:"custom"`
	SkinTones bool   `json:"skin_tones,omitempty"`
}

type conversationView struct {
	ID   string
	Name string
	// MarkedName is set only for a search result; see memberView.MarkedName.
	MarkedName    template.HTML
	Current       bool
	UnreadCount   int
	IsGroupDirect bool
	RecentAt      time.Time
	OpenUsers     string
	HasDraft      bool
}

type conversationDetailsView struct {
	ID        string
	Name      string
	IsChannel bool
	// IsPrivate and CanChangeVisibility drive the administrator's conversion
	// control. Going public is the one direction that cannot be undone in
	// effect, so the control says so before it is used.
	IsPrivate           bool
	CanChangeVisibility bool
	Topic               string
	Purpose             string
	Type                string
	Archived            bool
	Members             []memberView
	Invitees            []memberView
	Truncated           bool
	CloseURL            string
	CanEdit             bool
	CanInvite           bool
	CanAddPeople        bool
	CanConvert          bool
	CanLeave            bool
	CanClose            bool
	CanArchive          bool
	CanNotify           bool
	NotificationLevel   string
	FollowEveryThread   bool
	ArchiveVerb         string
	// Slack Connect. Connected names the organizations already in the channel;
	// Outstanding names the invitations still waiting on someone, so the panel
	// distinguishes "shared with" from "asked to share with" rather than
	// implying an invitation is a connection.
	Connected   []connectOrganizationView
	Outstanding []connectInviteView
	CanConnect  bool
	// Retention. RetentionDays is the duration that actually governs, so the
	// form opens on what is being applied rather than on an empty box.
	CanSetRetention  bool
	RetentionDays    int
	RetentionCustom  bool
	RetentionSummary string
	ConnectURL       string
	ConnectHosts     []conversationView
}

type connectOrganizationView struct {
	ID   string
	Name string
}

type connectInviteView struct {
	ID     string
	Target string
	Status string
	// Expires is the deadline and Expired says whether it has passed. Both are
	// needed: the list used to render "valid until <date>" from Expires alone,
	// which for a lapsed invitation stated the opposite of the truth beside a
	// control that would act on it.
	Expired    bool
	Expires    string
	CanApprove bool
	CanRevoke  bool
	ApproveURL string
	DenyURL    string
}

type pageData struct {
	// BrowserNotifications and NotificationsPaused are this product's two
	// halves of whether a desktop notification may be raised. The third — the
	// browser's permission — only the client can see.
	BrowserNotifications bool
	NotificationsPaused  bool
	Workspaces           []workspaceChoice
	Huddle               huddleView
	HuddleURL            string
	Timeline             messageList
	Thread               messageList
	ThreadTimestamp      string
	Channels             []conversationView
	Directs              []conversationView
	MoreChannelsURL      string
	Channel              string
	ChannelName          string
	ChannelPrefix        string
	// ChannelStatusDisplay is the other person's current status emoji resolved
	// to a glyph, shown beside a one-to-one DM's title the way it is shown beside
	// their name everywhere else. Empty for a channel or a group DM, which are
	// not a single person. ChannelStatusText is its tooltip prose.
	ChannelStatusDisplay template.HTML
	ChannelStatusText    string
	ChannelMeta          string
	// MemberCount is how many people are in the conversation, shown in the
	// header the way Slack shows it. Zero hides it: a count could not be read,
	// or the conversation is a DM, whose header is the person rather than a
	// population.
	MemberCount   int
	WorkspaceName string
	CSRFToken     string
	// Typing is rendered empty on first paint and filled by the live stream.
	// Reading it here instead would put a store query on every page render to
	// answer a question that is almost always "nobody", and the stream reads it
	// on its own first pass anyway.
	Typing typingView
	// Assistant is the state an assistant app has set on the open thread. It is
	// empty for every thread no app has touched, which is almost all of them.
	Assistant   assistantThreadView
	ShowProfile bool
	// ShowAdmin gates workspace administration; ShowAuthAdmin gates the
	// identity-provider page, which needs a provider to have anything to say.
	ShowAdmin     bool
	ShowAuthAdmin bool
	// Keyboard is the client's whole keyboard layer, rendered into the help
	// dialog Command/Control+/ opens. It comes from keyboardSections so the
	// dialog cannot describe a binding the page does not announce.
	Keyboard       []keyboardSectionView
	ReminderUnread bool
	// CanvasURL opens this conversation's own canvas. Slack gives every channel
	// one canvas of its own, which is not the same thing as a canvas shared
	// into it; it is empty until somebody writes in it, and it is offered only
	// to members, because only a member may read or create one.
	CanvasURL   string
	IsMember    bool
	CanPost     bool
	CanSchedule bool
	CanUpload   bool
	CanJoin     bool
	CanCreate   bool
	JoinURL     string
	Username    string
	UserInitial string
	OlderURL    string
	// LatestURL is set when the rendered window is not the newest one. It is
	// both the "jump to the latest messages" pager and the composer's
	// data-newest, so a post made while reading older history takes the
	// reader to where the message actually landed instead of refreshing a
	// window that cannot hold it. These were two fields assigned the same
	// value, which is one drift away from the pager and the composer
	// disagreeing about where "latest" is.
	LatestURL         string
	MarkReadURL       string
	MarkReadTimestamp string
	AtLatest          bool
	Notice            string
	Error             string
	Draft             string
	DraftAttachments  []draftAttachmentView
	DraftJSON         string
	ScheduleAt        string
	ComposeURL        string
	DraftURL          string
	ScheduleURL       string
	UploadURL         string
	StageUploadURL    string
	TimelineURL       string
	ThreadURL         string
	ThreadFollowURL   string
	FollowingThread   bool
	GlobalShortcuts   []domain.AppShortcut
	SlashCommands     []domain.AppShortcut
	ComposerMembers   []memberView
	ComposerGroups    []userGroupView
	ComposerChannels  []conversationView
	Apps              []domain.InstalledApp
	Modal             *modalView
	Details           *conversationDetailsView
}

type memberView struct {
	ID   string
	Name string
	// MarkedName is Name with the search terms emphasised. It is a separate
	// field rather than a marked Name because this view is also a picker option
	// and a sidebar entry, where there is nothing to mark and an HTML-typed name
	// would invite a caller to render it somewhere it does not belong.
	MarkedName template.HTML
	RealName   string
	Profile    domain.UserProfile
	// StatusDisplay is Profile.StatusEmoji resolved to a rendered glyph. The
	// stored value is a shortcode (":wave:"); rendering it raw showed the colons.
	StatusDisplay template.HTML
	Presence      string
	AvatarURL     string
	AuthorInitial string
	IsSelf        bool
	// IsVIP is whether the viewing member has marked this person a VIP, so the
	// directory can offer to add or remove them.
	IsVIP bool
}

type userGroupView struct {
	ID          string
	Name        string
	Handle      string
	Description string
	MemberCount int
}

type membersData struct {
	Members        []memberView
	Profile        domain.UserProfile
	StatusDisplay  template.HTML
	Presence       string
	StatusExpires  int64
	AvatarURL      string
	UserInitial    string
	CSRFToken      string
	Error          string
	CanEditProfile bool
	CanMessage     bool
	MoreMembersURL string
	Scheduled      []scheduledStatusView
	DraftScheduled scheduledStatusView
}

type scheduledStatusView struct {
	ID          string
	StatusText  string
	StatusEmoji string
	StartsAt    int64
	EndsAt      int64
}

type directMessagesData struct {
	Query      string
	Recent     []conversationView
	Members    []memberView
	CSRFToken  string
	Error      string
	CanMessage bool
}

type documentsData struct {
	Kind      string
	Title     string
	CSRFToken string
	CanWrite  bool
	Canvases  []documentCardView
	Lists     []documentCardView
	MoreURL   string
	Notice    string
}

type documentCardView struct {
	ID        string
	Title     string
	Preview   string
	URL       string
	UpdatedAt string
}

// assistantThreadView is what a member sees of an assistant's own state: a
// title for the thread, a transient status, and prompts they can send with one
// click. Present distinguishes "no assistant has touched this thread" from
// "an assistant set everything to empty", which render differently.
type assistantThreadView struct {
	Present      bool
	Title        string
	Status       string
	PromptsTitle string
	Prompts      []assistantPromptView
}

type assistantPromptView struct {
	Title   string
	Message string
}

type canvasCommentView struct {
	ID          string
	AuthorName  string
	SectionID   string
	SectionName string
	Text        string
	DisplayTime string
	MachineTime string
	DeleteURL   string
}

type canvasRevisionView struct {
	Version     int64
	Title       string
	Excerpt     string
	EditorName  string
	DisplayTime string
	MachineTime string
	RestoreURL  string
}

// channelCanvasData is the empty state of a conversation's own canvas: the one
// moment it has nothing to show, because it does not exist yet.
type channelCanvasData struct {
	Channel     string
	ChannelName string
	CSRFToken   string
	CanCreate   bool
	Notice      string
}

type canvasData struct {
	ID    string
	Title string
	// Sections is the canvas as it is stored. A canvas with several sections
	// used to be flattened into one blob of joined text and marked read-only,
	// so a canvas an app created through canvases.create — which has taken
	// structured sections since it was built — could be read but never edited,
	// and its structure was invisible.
	Sections       []canvasSectionView
	UpdatedAt      string
	CSRFToken      string
	CanWrite       bool
	CanDelete      bool
	ReadOnlyReason string
	Notice         string
	// Comments are the remarks on this canvas, oldest first. They are shown to
	// anyone who may read it, and anyone who may read it may add one: a canvas
	// shared for review that only its editors could discuss would make review
	// impossible.
	Comments []canvasCommentView
	// Revisions is what the canvas said before, newest first. It is shown to
	// anyone who may read the canvas, because a revision is the same document
	// at an earlier moment; restoring one needs write access, so the control
	// appears only where the edit would be accepted.
	Revisions []canvasRevisionView
	// Grants are who this canvas is shared with. Anyone who may open it sees
	// them: they can already see who commented on it and who edited it, so who
	// else may open it is not a further secret — and someone about to share it
	// needs to know it is not already shared.
	Grants []grantView
	// ShareTargets are the people and channels this canvas is not shared with
	// yet. Offering one it is already shared with would make a control whose
	// only effect is to change a level, which the level control already does.
	ShareTargets []shareTargetView
	// CanShare is the owner's alone. Granting is the strongest operation on a
	// canvas, so write access does not put the form on the page.
	CanShare bool
	// SharePath is this document's own path, which the shared sharing markup
	// posts beside rather than knowing the route for. ShareNoun is what the
	// prose calls it.
	SharePath string
	ShareNoun string
}

// grantView is one line of a sharing list. Target is what the revoke form posts
// back, "user:U1" or "channel:C1"; it is empty for a grant this client cannot
// revoke, and the row says why instead of drawing a control that would be
// refused.
type grantView struct {
	Name   string
	Access string
	Target string
	Reason string
}

type shareTargetView struct {
	Value string
	Name  string
	Kind  string
}

// documentGrant is a grant with its document forgotten. Canvases and lists
// carry the same grant model and want the same sharing surface, and the surface
// does not care which kind of document it is describing.
type documentGrant struct {
	EntityType domain.GrantEntity
	EntityID   string
	Access     domain.AccessLevel
}

type canvasSectionView struct {
	ID       string
	Type     string
	Text     string
	Editable bool
	Position int
}

type listCellView struct {
	ColumnName string
	Value      string
}

type listColumnView struct {
	Key  string
	Name string
	Type string
	// Primary marks the column that names the item. It cannot be removed, and
	// the row says why rather than offering a control the service refuses.
	Primary bool
	Options []string
}

type listData struct {
	ID       string
	Name     string
	TodoMode bool
	Items    []listItemView
	// Columns are the list's declared structure. Empty for an unstructured
	// list, which is what a list created without a schema is.
	Columns []listColumnView
	// Members are who an item may be assigned to: the people who can already
	// open this list. Offering anyone else would build a control that produces
	// a refusal, which the universal contract forbids.
	Members   []memberView
	MoreURL   string
	CSRFToken string
	CanWrite  bool
	Notice    string
	// View is the layout the reader chose: "list" (a single ordered list, the
	// default) or "board" (items grouped into lanes). BoardActive is View ==
	// "board" resolved against availability — a board needs a groupable column,
	// and asking for one on a list without one falls back to the list.
	View        string
	BoardActive bool
	ListViewURL string
	// BoardViewURL is empty when no column can group the list into lanes, which
	// is how the template knows not to offer a board that would have one lane.
	BoardViewURL string
	// GroupChoices are the columns the board can group by, each with the link
	// that regroups by it; GroupName is the one in effect. Lanes are the grouped
	// items; BoardTruncated says the list ran past listViewItemCap and the lanes
	// stop short.
	GroupChoices   []groupChoiceView
	GroupName      string
	Lanes          []listLaneView
	BoardTruncated bool
	// TableActive, TableViewURL, TableHeaders, SortKey and SortDir drive the
	// table layout: a real table whose column headers sort the rows. It shares
	// BoardTruncated's honesty about the read cap through the same field.
	TableActive  bool
	TableViewURL string
	TableHeaders []tableHeaderView
	SortKey      string
	SortDir      string
	// GroupKey, FilterOptions and FilterActive drive the filter control and let
	// the filter form re-submit the current view, group, and sort as hidden
	// fields, so narrowing the list keeps the layout the reader was in.
	GroupKey       string
	FilterOptions  []filterOptionView
	FilterActive   bool
	ClearFilterURL string
	// CalendarActive and its neighbours drive the calendar layout: a month grid
	// placing items by a date column, with a month stepper and a date-column
	// chooser when the list has more than one date column.
	CalendarActive  bool
	CalendarViewURL string
	CalendarWeeks   [][]calendarDayView
	MonthLabel      string
	PrevMonthURL    string
	NextMonthURL    string
	DateChoices     []groupChoiceView
	DateColumnName  string
	// Grants, ShareTargets, CanShare, SharePath and ShareNoun drive the sharing
	// section this page shares with the canvas, by the same two rules: anyone
	// who may open the list sees who else may, and only its owner changes that.
	Grants       []grantView
	ShareTargets []shareTargetView
	CanShare     bool
	SharePath    string
	ShareNoun    string
}

type listItemView struct {
	ID    string
	Title string
	// Cells are the item's values under the list's declared columns, in the
	// order the columns were declared. A structured list shows these instead of
	// a bare title, because the columns are what somebody said the list was for.
	Cells    []listCellView
	Archived bool
	// AssigneeName is who the item is for, resolved for display. AssigneeID is
	// what the form posts back, because a name is not an identity.
	AssigneeID   string
	AssigneeName string
	DueDate      string
	Overdue      bool
	// ListID, CanWrite, CSRFToken and Members carry the page context each row
	// needs to render its own action forms, so the same row markup serves both
	// the list and every board lane without the template reaching for a page root
	// a {{define}} block does not have. Members is the shared assignee slice —
	// assigning it copies a slice header, not the people.
	ListID    string
	CanWrite  bool
	CSRFToken string
	Members   []memberView
}

type directExpansionReviewData struct {
	SourceID       string
	SourceName     string
	Additions      []memberView
	History        string
	IncludeHistory bool
	ChooseHistory  bool
	CSRFToken      string
}

type searchData struct {
	Query                string
	Channel              string
	Type                 string
	Sort                 string
	Direction            string
	Messages             []messageView
	Files                []searchFileView
	People               []memberView
	Canvases             []searchCanvasView
	Lists                []searchListView
	Conversations        []conversationView
	Tabs                 []searchTabView
	ConversationOptions  []conversationView
	MemberOptions        []memberView
	SelectedConversation string
	SelectedMember       string
	After                string
	Before               string
	Has                  string
	CurrentOnly          bool
	ResultCount          int
	Error                string
	MoreURL              string
	Warning              string
	Recent               []searchHistoryView
	Searched             bool
}

// scheduleDayView is one weekday checkbox. The label is the English name and
// the value is Go's own weekday number, which is what the row stores — a form
// that posted names would have to agree with the store about spelling.
type scheduleDayView struct {
	Number   int
	Label    string
	Selected bool
}

func scheduleDayViews(selected []time.Weekday) []scheduleDayView {
	chosen := make(map[time.Weekday]struct{}, len(selected))
	for _, day := range selected {
		chosen[day] = struct{}{}
	}
	views := make([]scheduleDayView, 0, 7)
	for day := time.Sunday; day <= time.Saturday; day++ {
		_, ok := chosen[day]
		views = append(views, scheduleDayView{Number: int(day), Label: day.String(), Selected: ok})
	}
	return views
}

// minutesAsClock renders a minute-of-day for an <input type="time">, which is
// the control that already knows how to enter one.
func minutesAsClock(minute int) string {
	if minute < 0 || minute >= domain.NotificationScheduleDayMinutes {
		return "00:00"
	}
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}

// clockAsMinutes reads one back. A value the browser did not produce is
// refused rather than coerced: a schedule silently starting at midnight
// because the field was malformed is worse than a form that says no.
func clockAsMinutes(value string) (int, bool) {
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed.Hour()*60 + parsed.Minute(), true
}

type searchHistoryView struct {
	Query string
	URL   string
}

// searchCanvasView carries a snippet rather than the document. A canvas can be
// long, and a result list that rendered whole documents would bury the other
// results; a snippet around nothing in particular is still enough to recognise
// the canvas you meant.
type searchCanvasView struct {
	ID          string
	Title       template.HTML
	Snippet     template.HTML
	Owner       string
	DisplayTime string
	MachineTime string
	URL         string
}

type searchListView struct {
	ID          string
	Title       template.HTML
	Snippet     template.HTML
	Owner       string
	DisplayTime string
	MachineTime string
	URL         string
}

type searchTabView struct {
	Label   string
	URL     string
	Current bool
}

type searchFileView struct {
	ID          string
	Name        template.HTML
	Title       template.HTML
	MIMEType    string
	Size        string
	Uploader    string
	DisplayTime string
	MachineTime string
	DownloadURL string
}

type searchSuggestion struct {
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

type searchSuggestionsResponse struct {
	Items []searchSuggestion `json:"items"`
}

type activityData struct {
	Channel     string
	CSRFToken   string
	Items       []activityItemView
	Filters     []activityFilterView
	SavedViews  []activitySavedViewView
	KindOptions []activityKindOption
	Layout      string
	UnreadOnly  bool
	ClearedOnly bool
	Kind        string
	View        string
	CreateURL   string
	DeleteURL   string
	UnreadURL   string
	ClearedURL  string
	ActiveURL   string
	MoreURL     string
	Notice      string
}

type activityFilterView struct {
	Label   string
	URL     string
	Current bool
}

// activitySavedViewView is one of a member's saved Activity filters as a tab:
// where it links, how to remove it, and whether it is the one being shown.
type activitySavedViewView struct {
	ID      string
	Name    string
	URL     string
	Current bool
}

type activityKindOption struct {
	Value string
	Label string
}

type activityItemView struct {
	ID          string
	KindLabel   string
	ActorName   string
	ChannelName string
	Text        template.HTML
	MachineTime string
	DisplayTime string
	SourceURL   string
	ReplyURL    string
	ReactionURL string
	Unread      bool
	Cleared     bool
	Unavailable bool
}

type notificationExceptionView struct {
	ID                string
	Name              string
	Prefix            string
	Level             string
	FollowEveryThread bool
	URL               string
}

type notificationsData struct {
	Channel           string
	CSRFToken         string
	Level             string
	Keywords          string
	ActivityChannels  bool
	ActivityReminders bool
	// BrowserNotifications is this product's half of the decision. The other
	// half belongs to the browser, and the page says which of the two is
	// missing rather than reporting one silent "off" for both.
	BrowserNotifications     bool
	BrowserNotificationState string
	Snoozed                  bool
	ScheduleEnabled          bool
	ScheduleSuppressing      bool
	ScheduleStart            string
	ScheduleEnd              string
	ScheduleZone             string
	ScheduleDays             []scheduleDayView
	SnoozeUntil              string
	Exceptions               []notificationExceptionView
	Notice                   string
}

type scheduledMessageView struct {
	ID              string
	Text            string
	DisplayText     string
	MachineTime     string
	DisplayTime     string
	ChannelName     string
	ChannelPrefix   string
	ConversationURL string
	CancelURL       string
	UpdateURL       string
	SendNowURL      string
	Status          string
	Failure         string
	AttachmentCount int
}

type draftView struct {
	Text            string
	MachineTime     string
	DisplayTime     string
	ChannelName     string
	ChannelPrefix   string
	OpenURL         string
	DeleteURL       string
	AttachmentCount int
}

type draftAttachmentView struct {
	UploadID string `json:"upload_id"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

func newDraftAttachmentViews(values []domain.DraftAttachment) []draftAttachmentView {
	result := make([]draftAttachmentView, 0, len(values))
	for _, value := range values {
		result = append(result, draftAttachmentView{
			UploadID: string(value.UploadID), Name: value.Name, Title: value.Title,
			MIMEType: value.MIMEType, Size: value.Size,
		})
	}
	return result
}

func draftAttachmentsFromJSON(raw string) ([]domain.DraftAttachment, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var values []draftAttachmentView
	if err := json.Unmarshal([]byte(raw), &values); err != nil || len(values) > 10 {
		return nil, service.ErrInvalidExternalUpload
	}
	result := make([]domain.DraftAttachment, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.UploadID) == "" {
			return nil, service.ErrInvalidExternalUpload
		}
		result = append(result, domain.DraftAttachment{UploadID: domain.ExternalUploadID(value.UploadID), Title: value.Title})
	}
	return result, nil
}

type sentMessageView struct {
	Text          string
	MachineTime   string
	DisplayTime   string
	ChannelName   string
	ChannelPrefix string
	OpenURL       string
}

type draftsAndSentData struct {
	Channel   string
	CSRFToken string
	ActiveTab string
	Drafts    []draftView
	Scheduled []scheduledMessageView
	Sent      []sentMessageView
	MoreURL   string
	MoreLabel string
	Notice    string
}

type laterItemView struct {
	ID              string
	Text            string
	AuthorName      string
	MachineTime     string
	DisplayTime     string
	ChannelName     string
	ChannelPrefix   string
	SourceURL       string
	SourceAvailable bool
	CompleteURL     string
	ArchiveURL      string
	RestoreURL      string
	RemoveURL       string
}

type laterData struct {
	Channel           string
	CSRFToken         string
	State             domain.SavedItemState
	StateTitle        string
	InProgressCurrent bool
	ArchivedCurrent   bool
	CompletedCurrent  bool
	Items             []laterItemView
	Reminders         []laterReminderView
	RemindersOnly     bool
	ChannelReminders  bool
	MoreURL           string
	Notice            string
}

type laterReminderView struct {
	ID          string
	Text        string
	MachineTime string
	DisplayTime string
	Recurrence  string
	SourceURL   string
	SourceLabel string
	Delivered   bool
	Completed   bool
	Failed      bool
	FailureCode string
	UpdateURL   string
	CompleteURL string
	DeleteURL   string
	CanEdit     bool
	CanComplete bool
	DateValue   string
	TimeValue   string
	TimeZone    string
}

type identityData struct {
	Heading   string
	Username  string
	Email     string
	Role      string
	Release   string
	CSRFToken string
	AvatarURL string
	Avatar    string
}

type errorData struct {
	Heading string
	Message string
}

type oauthConsentData struct {
	Action              string
	AppName             string
	BotScopes           []scopeConsentView
	UserScopes          []scopeConsentView
	CSRFToken           string
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// scopeConsentView is one requested scope on the install-consent screen: the
// human sentence a person reads and the raw token that is actually granted. The
// token stays visible beside the sentence — it is what the app receives and what
// the hidden form submits — but it is no longer the only thing shown.
type scopeConsentView struct {
	Name        string
	Description string
}

// scopeConsentViews pairs each granted scope token with its description. An
// unknown scope keeps an empty description, and the template shows its token
// alone rather than inventing a meaning.
func scopeConsentViews(scopes []string) []scopeConsentView {
	views := make([]scopeConsentView, 0, len(scopes))
	for _, scope := range scopes {
		views = append(views, scopeConsentView{Name: scope, Description: auth.Scope(scope).Description()})
	}
	return views
}

// ---------------------------------------------------------------------------
// Templates
//
// Every page is rendered from one layout with one palette and one theme
// bootstrap. The layout owns the document, the shared stylesheet and the theme
// script; each page contributes a "title", a "content" and optionally "styles"
// and "scripts". Nothing is patched into the rendered HTML afterwards, so a
// template edit can no longer take a page out of service.
// ---------------------------------------------------------------------------

// The palette carries three tokens that exist because a colour that reads well
// as text does not read well as a background:
//
//   - --on-strong is the text colour used on a --ok or --danger *background*.
//     The dark palette tunes --ok and --danger as foreground colours, so white
//     on them measured 2.31:1 (the Send button) and 1.96:1 (Sign out) — both
//     below SC 1.4.3. The dark value of --on-strong takes them to 7.8:1 and
//     9.2:1.
//   - --field-line is the border of a control, which SC 1.4.11 holds to 3:1.
//     --line is a decorative separator at 1.35:1 and cannot serve both.
//   - --focus-chrome is the focus ring over the purple topbar and sidebar,
//     where --focus measures 1.65:1 — and where every primary control lives.
const lightTokens = `color-scheme:light;--bg:#fff;--panel:#f7f5f8;--panel-strong:#fff;--text:#1d1c1d;--muted:#5b565c;--line:#d9d4da;--field-line:#6b6570;--accent:#611f69;--chrome:#3f0e40;--chrome-top:#350d36;--chrome-line:#ffffff1f;--chrome-muted:#cfc3d0;--on-accent:#fff;--on-strong:#fff;--action:#5c1a64;--hover:#f1edf2;--focus:#0b5cad;--focus-chrome:#fff;--danger:#a01133;--danger-bg:#fdeef1;--ok:#0a6b4f;--mark-bg:#fbe9a8;--shadow:0 8px 24px #1d1c1d1f`

const darkTokens = `color-scheme:dark;--bg:#1a1d21;--panel:#222529;--panel-strong:#1e2125;--text:#e9e7ea;--muted:#aca7ae;--line:#3b3f45;--field-line:#8a8f96;--accent:#4a1750;--chrome:#231e26;--chrome-top:#1b171d;--chrome-line:#ffffff17;--chrome-muted:#b9b2bb;--on-accent:#fff;--on-strong:#141719;--action:#8fd7f4;--hover:#2c3035;--focus:#7cc4ff;--focus-chrome:#fff;--danger:#ff9db4;--danger-bg:#3a1622;--ok:#3fbf95;--mark-bg:#5a4a12;--shadow:0 8px 24px #0006`

const sharedStyle = `*{box-sizing:border-box}
:root{` + lightTokens + `}
html[data-theme=dark]{` + darkTokens + `}
@media(prefers-color-scheme:dark){html[data-theme=light]:not([data-theme-explicit]){` + darkTokens + `}}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
button,input,textarea{font:inherit}
button{cursor:pointer}
a{color:var(--action)}
:focus-visible{outline:3px solid var(--focus);outline-offset:2px}
.topbar :focus-visible,.sidebar :focus-visible,.bar :focus-visible{outline-color:var(--focus-chrome)}
.visually-hidden{position:absolute;width:1px;height:1px;margin:-1px;padding:0;overflow:hidden;clip:rect(0 0 0 0);clip-path:inset(50%);white-space:nowrap;border:0}
.skip-link{position:absolute;left:8px;top:-48px;z-index:9;background:var(--panel-strong);color:var(--action);border:1px solid var(--line);border-radius:0 0 6px 6px;padding:8px 12px;text-decoration:none}
.skip-link:focus{top:0}
.notice{margin:0;padding:8px 12px;background:var(--panel);border:1px solid var(--line);border-radius:6px;color:var(--text);font-size:13px}
.form-error{margin:0 0 10px;padding:10px 12px;background:var(--danger-bg);border:1px solid var(--danger);border-radius:6px;color:var(--danger);font-weight:700}
.theme-toggle{border:1px solid #ffffffb8;border-radius:5px;color:inherit;background:transparent;padding:6px 9px}
.theme-toggle:hover{background:#ffffff2b}
.theme-toggle[aria-pressed=true]{background:#ffffff42}
.nav-toggle,.nav-scrim{display:none}
.pager{margin:0;padding:6px 0;text-align:center;font-size:13px}
@media(prefers-reduced-motion:reduce){*{animation-duration:.01ms !important;animation-iteration-count:1 !important;transition-duration:.01ms !important;scroll-behavior:auto !important}}`

// themeBootstrap resolves the theme before the first paint, so a stored or
// operating-system dark preference never flashes the light palette.
const themeBootstrap = `<script>(function(){var root=document.documentElement;var dark=false;root.classList.add('js');try{var stored=localStorage.getItem('sameoldchat-theme');dark=stored?stored==='dark':!!(window.matchMedia&&window.matchMedia('(prefers-color-scheme: dark)').matches)}catch(error){dark=false}root.setAttribute('data-theme',dark?'dark':'light');root.setAttribute('data-theme-explicit','')})();</script>`

const themeToggleScript = `<script>(function(){var root=document.documentElement;var toggle=document.getElementById('theme-toggle');function apply(theme){root.setAttribute('data-theme',theme);root.setAttribute('data-theme-explicit','');if(toggle)toggle.setAttribute('aria-pressed',theme==='dark'?'true':'false')}apply(root.getAttribute('data-theme')==='dark'?'dark':'light');if(!toggle)return;toggle.addEventListener('click',function(){var next=root.getAttribute('data-theme')==='dark'?'light':'dark';apply(next);try{localStorage.setItem('sameoldchat-theme',next)}catch(error){}})})();</script>`

const layoutMarkup = `<!doctype html>
<html lang="en" data-theme="light"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{template "title" .}}</title><style>` + sharedStyle + `</style>{{block "styles" .}}{{end}}` + themeBootstrap + `</head><body>{{template "content" .}}` + themeToggleScript + `{{block "scripts" .}}{{end}}</body></html>`

// templateFunctions is deliberately tiny: it exists so a template cannot write
// an aria-keyshortcuts value by hand. Every advertised chord is looked up in
// keyboardSections, which is what keeps the announced binding, the documented
// binding and the implemented binding the same thing.
var templateFunctions = template.FuncMap{"ariaKeyshortcuts": ariaKeyshortcuts}

var layoutTemplate = template.Must(template.New("layout").Funcs(templateFunctions).Parse(layoutMarkup))

func mustPage(markup string) *template.Template {
	return template.Must(template.Must(layoutTemplate.Clone()).Parse(markup))
}

const pageStyle = `<style>
.shell{height:100vh;display:grid;grid-template-rows:52px minmax(0,1fr)}
.topbar{background:var(--chrome-top);color:var(--on-accent);display:flex;align-items:center;gap:12px;padding:0 16px;box-shadow:none}
.brand{font-weight:800;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.search{position:relative;flex:1 1 auto;min-width:0;max-width:560px;margin:auto;display:flex;align-items:center;gap:8px;background:#ffffff2b;border:1px solid #ffffff8a;border-radius:7px;padding:4px 10px}
.search input[name=q]{flex:1 1 auto;min-width:0;border:0;outline:0;background:transparent;color:var(--on-accent)}
.search input[name=q]::placeholder{color:#ffffffd6}
.search-submit{border:0;background:transparent;color:var(--on-accent);font-weight:700;padding:2px 2px}
.search-suggestions{position:absolute;z-index:30;top:calc(100% + 6px);left:0;right:0;max-height:min(420px,70vh);overflow:auto;padding:6px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow)}
.search-suggestions[hidden]{display:none}.search-suggestion{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:4px 12px;padding:8px 10px;border-radius:6px;color:var(--text);text-decoration:none}.search-suggestion:hover,.search-suggestion[aria-selected=true]{background:var(--hover)}.search-suggestion-label{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700}.search-suggestion-kind{color:var(--muted);font-size:12px;text-transform:capitalize}
.top-actions{display:flex;align-items:center;gap:8px;margin-left:auto;flex:0 0 auto}
.icon-button{border:0;background:transparent;color:var(--on-accent);border-radius:6px;padding:7px 9px;text-decoration:none}
.icon-button:hover{background:#ffffff2b}
.workspace{display:grid;grid-template-columns:256px minmax(0,1fr);min-height:0}
.sidebar{background:var(--chrome);color:var(--on-accent);padding:8px 0 16px;display:flex;flex-direction:column;gap:2px;overflow:auto}
.workspace-name{font-weight:800;padding:0 10px}
.connect-invites{list-style:none;margin:8px 0;padding:0;display:grid;gap:8px}
.connect-invites li{display:flex;flex-wrap:wrap;gap:8px;align-items:center;justify-content:space-between;padding:8px;border:1px solid var(--line);border-radius:8px}
.connect-actions{display:flex;gap:8px}
.connect-invite{display:flex;flex-wrap:wrap;gap:8px;align-items:end;margin-top:8px}
.connect-invite label{display:grid;gap:4px;font-size:12px;color:var(--muted)}
.workspace-switch summary{cursor:pointer;list-style:none}
.workspace-switch summary::-webkit-details-marker{display:none}
.workspace-switch summary::after{content:" ▾"}
.workspace-list{list-style:none;margin:6px 0 0;padding:0;display:grid;gap:2px}
.workspace-list button{width:100%;text-align:left;border:0;border-radius:5px;background:transparent;color:var(--on-accent);padding:7px 10px;font:inherit}
.workspace-list button:hover{background:#ffffff2b}
.workspace-list small{display:block;color:#e8cbe9;font-weight:400}
.workspace-current{display:block;padding:7px 10px;border-radius:5px;background:#ffffff2b;font-weight:700}
.workspace-current small{display:block;color:#e8cbe9;font-weight:400}
.workspace-sub{color:#e8cbe9;font-size:12px;padding:2px 10px}
.side-section{display:grid;gap:2px}
.side-label{color:var(--chrome-muted);font-size:13px;font-weight:700;padding:14px 16px 4px;text-transform:none;letter-spacing:0}
.side-link{display:flex;align-items:center;gap:9px;width:100%;min-height:28px;padding:2px 16px;border:0;border-radius:0;background:transparent;color:var(--chrome-muted);font:inherit;line-height:1.3;text-align:left;text-decoration:none}
.side-link:hover{background:#ffffff14;color:var(--on-accent)}
.side-link[aria-current=page]{background:#1164a3;color:#fff}
.side-link[aria-current=page]{font-weight:700}
.side-icon{flex:0 0 auto;display:inline-block;min-width:1em;text-align:center}
.side-text{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.side-empty{margin:0;padding:6px 10px;color:#e8cbe9;font-size:13px}
.side-more{padding:6px 10px;color:var(--on-accent);font-size:13px}
.badge{margin-left:auto;background:var(--on-accent);color:var(--accent);border-radius:12px;min-width:20px;text-align:center;padding:1px 6px;font-size:12px;font-weight:800}
.draft-badge{margin-left:auto;color:#f3e3f4;font-size:11px;font-style:italic}
.sidebar-bottom{margin-top:auto;border-top:1px solid #ffffff5c;padding-top:12px}
.signed-in{display:flex;align-items:center;gap:9px;padding:4px 10px 10px;min-width:0}
.signed-in-avatar{flex:0 0 auto;width:24px;height:24px;border-radius:5px;display:grid;place-items:center;background:#ffffff42;font-size:11px;font-weight:800;text-transform:uppercase;overflow:hidden}
.signed-in-name{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700}
.content{min-width:0;min-height:0;display:grid;grid-template-rows:auto minmax(0,1fr) auto;{{if .ThreadTimestamp}}grid-template-columns:minmax(0,1fr) minmax(0,360px);grid-template-areas:"head thread" "timeline thread" "composer thread"{{else}}grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "timeline" "composer"{{end}}}
.channel-header{grid-area:head;display:flex;align-items:center;gap:16px;flex-wrap:wrap;border-bottom:1px solid var(--line);padding:12px 26px}
.channel-title{margin:0;font-size:18px;font-weight:800}
.channel-meta{margin:2px 0 0;color:var(--muted);font-size:13px}
.channel-actions{margin-left:auto;display:flex;align-items:center;gap:12px;font-size:13px}
.action-feedback{margin:0;max-width:520px;padding:7px 10px;border:1px solid var(--danger);border-radius:6px;background:var(--danger-bg);color:var(--danger);font-weight:700}
.timeline-wrap{grid-area:timeline;min-height:0;display:grid;grid-template-rows:auto minmax(0,1fr) auto}
.pager-older{grid-row:1}
.timeline{grid-row:2;overflow:auto;padding:18px 26px 12px;scroll-behavior:smooth}
.pager-newer{grid-row:3}
.message{position:relative;display:grid;grid-template-columns:38px minmax(0,1fr);gap:10px;padding:6px 8px;border-radius:7px}
.message:hover{background:var(--hover)}
.message:focus{background:var(--hover);outline:3px solid var(--focus);outline-offset:-1px}
.message:target{background:var(--hover);outline:2px solid var(--focus)}
.avatar{height:36px;width:36px;border-radius:6px;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;display:grid;place-items:center;font-weight:800;font-size:15px;text-transform:uppercase;overflow:hidden}.avatar img{width:100%;height:100%;object-fit:cover}.avatar.avatar-emoji{font-size:10px;text-transform:none;overflow-wrap:anywhere}
.message-body{min-width:0}
.message-head{display:flex;align-items:center;gap:10px;flex-wrap:wrap}.app-label{padding:1px 4px;border-radius:3px;background:var(--hover);color:var(--muted);font-size:10px;font-weight:800;letter-spacing:.04em}
.author{font-weight:800}.author-status{display:inline-flex;align-items:center;margin-left:-4px}.author-status .standard-emoji,.author-status .custom-emoji{width:16px;height:16px;font-size:15px;line-height:16px}
.channel-title-status{display:inline-flex;align-items:center;vertical-align:middle;margin-left:6px}.channel-title-status .standard-emoji,.channel-title-status .custom-emoji{width:18px;height:18px;font-size:16px;line-height:18px}
a.time{display:inline-flex;align-items:center;min-height:24px;padding:0 4px;margin:0 -4px;border-radius:5px}a.time:hover{background:var(--hover)}
.time{color:var(--muted);font-size:12px}
.pinned{color:var(--muted);font-size:12px;font-weight:700}
.message-text{margin:2px 0 6px;white-space:pre-wrap;overflow-wrap:anywhere}
.message-files{display:grid;gap:7px;margin:7px 0;max-width:520px}
.message-file{display:flex;align-items:center;gap:10px;padding:9px 11px;border:1px solid var(--line);border-radius:8px;background:var(--panel)}
.message-file-icon{display:grid;place-items:center;width:34px;height:34px;border-radius:6px;background:var(--accent);color:var(--on-accent);font-size:11px;font-weight:800}
.message-file-copy{display:grid;min-width:0}.message-file-title{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:800}.message-file-meta{color:var(--muted);font-size:12px}
.upload-form{display:flex;align-items:end;gap:8px;flex-wrap:wrap;margin:7px 0}.upload-form label{display:grid;gap:3px;color:var(--muted);font-size:12px}.upload-form input{max-width:260px}
.message-blocks,.message-attachments,.message-unfurls{display:grid;gap:8px;margin:8px 0}
.message-block{white-space:pre-wrap;overflow-wrap:anywhere}
.formatted-text{white-space:normal;overflow-wrap:anywhere}.formatted-text>:first-child{margin-top:0}.formatted-text>:last-child{margin-bottom:0}.formatted-text p{margin:0 0 8px}.formatted-text h1,.formatted-text h2,.formatted-text h3,.formatted-text h4,.formatted-text h5,.formatted-text h6{margin:12px 0 6px;line-height:1.25}.formatted-text ul,.formatted-text ol{margin:6px 0;padding-left:24px}.formatted-text blockquote{margin:6px 0;padding-left:12px;border-left:4px solid var(--line);color:var(--muted)}.formatted-text pre{max-width:100%;overflow:auto;margin:6px 0;padding:10px;border-radius:6px;background:var(--hover);white-space:pre}.formatted-text code{padding:1px 3px;border-radius:3px;background:var(--hover);font-family:ui-monospace,SFMono-Regular,Consolas,monospace}.formatted-text pre code{padding:0;background:transparent}.formatted-text a{color:var(--action)}.slack-mention{padding:1px 3px;border-radius:3px;background:color-mix(in srgb,var(--action) 15%,transparent);color:var(--action)}
.message-block.header{font-size:17px;font-weight:800}
.message-block.context{color:var(--muted);font-size:12px}
.message-block.divider{border:0;border-top:1px solid var(--line);margin:5px 0}
.message-block-fields,.attachment-fields{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:6px 14px;margin:6px 0 0;padding:0;list-style:none}
.message-block-fields li,.attachment-fields li{white-space:pre-wrap}
.block-table-wrap{max-width:100%;overflow:auto}.block-table{border-collapse:collapse;width:max-content;min-width:100%;font-size:13px}.block-table th,.block-table td{border:1px solid var(--line);padding:6px 9px;vertical-align:top;white-space:pre-wrap}.block-table th{background:var(--hover);font-weight:700;text-align:left}
.message-block.task-card{max-width:560px;padding:10px 12px;border:1px solid var(--line);border-radius:8px;background:var(--panel)}.message-block.task-card.error{border-color:var(--danger)}.message-block.task-card.plan{border-left:4px solid var(--accent)}.message-block.task-card.dense{padding:6px 10px;font-size:13px}.stream-task-title{display:flex;align-items:center;justify-content:space-between;gap:12px}.stream-task-status{color:var(--muted);font-size:12px;font-weight:700}.stream-task-details,.stream-task-output{margin-top:6px;color:var(--muted)}.stream-task-sources{display:flex;flex-wrap:wrap;gap:8px;margin:6px 0 0;padding:0;list-style:none}.streaming-label{display:inline-flex;align-items:center;gap:5px;color:var(--muted);font-size:12px}.streaming-label::before{content:"";width:7px;height:7px;border-radius:50%;background:var(--ok);animation:stream-pulse 1.2s ease-in-out infinite}@keyframes stream-pulse{50%{opacity:.3}}
.message-block.alert{max-width:620px;padding:10px 12px;border-left:4px solid var(--muted);border-radius:5px;background:var(--hover)}.message-block.alert.info{border-color:#1264a3}.message-block.alert.warning{border-color:#d29b05}.message-block.alert.error{border-color:var(--danger)}.message-block.alert.success{border-color:var(--ok)}
.message-block.card{max-width:560px;padding:0;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden}.block-card-hero{display:block;width:100%;max-height:280px;object-fit:cover}.block-card-content{padding:12px}.block-card-heading{display:flex;align-items:flex-start;gap:9px}.block-card-heading>div{display:grid;gap:2px}.block-card-icon{width:36px;height:36px;border-radius:6px;object-fit:cover}.block-card-title,.block-card-subtitle{display:block}.block-card-subtitle,.block-card-subtext{color:var(--muted);font-size:13px}.block-card-body{margin-top:10px}.block-card-subtext{margin-top:8px}.message-block.carousel{max-width:min(720px,100%);overflow-x:auto;padding-bottom:6px}.block-carousel-track{display:flex;gap:10px;scroll-snap-type:x mandatory}.block-carousel-card{min-width:min(320px,80vw);max-width:360px;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden;scroll-snap-align:start}
.message-block.plan{max-width:620px}.block-plan{border:1px solid var(--line);border-radius:9px;background:var(--panel);overflow:hidden}.block-plan-title{display:block;padding:10px 12px;border-bottom:1px solid var(--line)}.block-plan-tasks{display:grid}.block-plan-task{padding:10px 12px;border-bottom:1px solid var(--line)}.block-plan-task:last-child{border-bottom:0}
.message-block.container{width:100%}.message-block.container.narrow{max-width:420px}.message-block.container.standard{max-width:620px}.message-block.container.wide{max-width:780px}.message-block.container.full{max-width:none}.block-container-frame{display:block;border:1px solid var(--line);border-radius:10px;background:var(--panel);overflow:hidden}.block-container-frame>summary,.block-container-frame>header{display:flex;align-items:center;gap:9px;padding:11px 13px}.block-container-frame>summary{cursor:pointer}.block-container-frame>summary::marker{color:var(--muted)}.block-container-frame>header.with-divider{border-bottom:1px solid var(--line)}.block-container-icon{width:36px;height:36px;border-radius:6px;object-fit:cover}.block-container-heading{display:grid;gap:2px}.block-container-heading>span{color:var(--muted);font-size:13px}.block-container-children{display:grid;gap:8px;padding:4px 13px 13px}.block-container-child{min-width:0}
.message-block.data-visualization{max-width:680px}.block-chart{margin:0;padding:12px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.block-chart>figcaption{margin-bottom:12px;font-weight:800}.block-chart-pie-layout{display:flex;align-items:center;gap:18px}.block-chart-pie-graphic{width:150px;aspect-ratio:1;border-radius:50%;box-shadow:inset 0 0 0 1px color-mix(in srgb,var(--fg) 12%,transparent);flex:0 0 auto}.block-chart-legend{display:flex;flex-wrap:wrap;gap:7px 14px;margin:10px 0 0;padding:0;list-style:none;font-size:12px}.block-chart-pie-layout>.block-chart-legend{display:grid;margin:0}.block-chart-legend li{display:flex;align-items:center;gap:6px}.block-chart-legend strong{margin-left:auto}.block-chart-swatch{width:9px;height:9px;border-radius:2px;flex:0 0 auto}.block-chart-bars{display:grid;gap:8px}.block-chart-bar-group{display:grid;grid-template-columns:minmax(90px,1fr) 3fr;align-items:center;gap:8px}.block-chart-category{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:var(--muted);font-size:12px}.block-chart-bar-series{display:grid;gap:3px}.block-chart-bar{display:block;min-width:2px;height:8px;border-radius:3px}.block-chart-svg{display:block;width:100%;height:auto;max-height:240px;overflow:visible}.block-chart-axis{stroke:var(--line);stroke-width:1}.block-chart-data{margin-top:10px;font-size:12px}.block-chart-data>summary{cursor:pointer;color:var(--muted)}@media(max-width:600px){.block-chart-pie-layout{align-items:flex-start;flex-direction:column}.block-chart-pie-graphic{width:120px}.block-chart-bar-group{grid-template-columns:80px 1fr}}
.message-block-actions{display:flex;flex-wrap:wrap;gap:6px;margin:5px 0 0;padding:0;list-style:none}
.ephemeral-label{color:var(--muted);font-size:12px;font-style:italic}
.message-block-actions form,.message-block-actions>div{display:inline-flex;align-items:center;gap:6px}
.block-action{border:1px solid var(--field-line);border-radius:5px;padding:5px 10px;background:var(--panel-strong);color:var(--text);font:inherit;font-weight:700}
.block-action:hover{background:var(--hover)}
.block-action:focus-visible{outline:3px solid var(--focus);outline-offset:2px}
.block-action-select{max-width:min(320px,70vw)}
.external-select{display:flex;flex-wrap:wrap;align-items:center;gap:6px}.external-select-status{width:100%;margin:0;color:var(--muted);font-size:12px}
.block-action-options{display:flex;flex-wrap:wrap;gap:5px 10px;margin:0;padding:0;border:0}
.block-action-options label{display:inline-flex;align-items:center;gap:4px}
.message-media{display:block;max-width:min(520px,100%);max-height:360px;margin-top:7px;border-radius:8px;object-fit:contain}
.message-attachment{border-left:4px solid var(--line);border-radius:4px;background:var(--panel-strong);padding:9px 12px;overflow-wrap:anywhere}
.message-attachment .pretext{margin:0 0 6px}
.message-attachment .attachment-author,.message-attachment .attachment-footer,.unfurl-source{color:var(--muted);font-size:12px}
.message-attachment .attachment-title{display:block;margin:2px 0;font-weight:800}
.message-attachment .attachment-text{margin:4px 0;white-space:pre-wrap}
.message-unfurls .message-attachment{border-left-color:var(--action)}
.reactions{display:flex;flex-wrap:wrap;gap:6px;margin:0 0 6px;padding:0;list-style:none}
.chip{display:inline-flex;gap:5px;align-items:center;border:1px solid var(--field-line);border-radius:12px;background:var(--panel);color:var(--text);padding:1px 9px;font-size:12px}
.chip[aria-pressed=true]{border-color:var(--action);font-weight:800}
.chip-count{font-variant-numeric:tabular-nums;font-weight:700}
.message-actions{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.message-actions a,.message-actions button{display:inline-flex;flex:0 0 auto;align-items:center;min-height:24px;color:var(--muted);background:transparent;border:0;padding:2px 4px;margin:0 -4px;border-radius:5px;text-decoration:none;font-size:12px}
.message-actions a:hover,.message-actions button:hover{color:var(--action);text-decoration:underline}
.inline-form{display:inline-flex;gap:6px;align-items:center}
.inline-form input[type=text]{width:130px;border:1px solid var(--field-line);border-radius:4px;background:var(--panel-strong);color:var(--text);padding:3px 6px}
.empty{color:var(--muted);padding:26px;text-align:center}
.composer-wrap{grid-area:composer;padding:8px 26px 18px}
.live-status{margin:0 0 6px;min-height:18px;color:var(--muted);font-size:12px}
.composer{border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:10px}
.composer.is-error{border-color:var(--danger)}
.composer textarea{width:100%;min-height:44px;resize:vertical;border:0;outline:0;background:transparent;color:var(--text)}
.composer-toolbar{display:flex;align-items:center;flex:1 1 auto;flex-wrap:wrap;gap:2px;border:0;padding:0;margin:0;position:relative}
.composer-tool,.composer-menu>summary{display:inline-flex;align-items:center;justify-content:center;min-width:30px;height:28px;border:0;border-radius:5px;background:transparent;color:var(--muted);font-weight:700;cursor:pointer;padding:0 7px}
.composer-tool:hover,.composer-tool:focus-visible,.composer-menu>summary:hover,.composer-menu>summary:focus-visible{background:var(--panel);color:var(--text)}
/* Slack keeps a channel's secondary actions behind one overflow control rather
   than spreading them across the header, so the header reads as the channel's
   name and topic first. */
.channel-meta .member-count{color:inherit;font-weight:600}
.channel-overflow{position:relative}
.channel-overflow>summary{display:inline-flex;align-items:center;justify-content:center;min-width:28px;height:26px;border-radius:5px;background:transparent;color:var(--muted);cursor:pointer;list-style:none;font-weight:700}
.channel-overflow>summary::-webkit-details-marker{display:none}
.channel-overflow>summary:hover,.channel-overflow>summary:focus-visible{background:var(--panel);color:var(--text)}
.channel-overflow[open]>.channel-overflow-menu{position:absolute;z-index:7;right:0;top:30px;display:grid;gap:2px;min-width:200px;border:1px solid var(--line);border-radius:7px;background:var(--panel-strong);box-shadow:var(--shadow);padding:5px}
.channel-overflow-menu button{width:100%;border:0;border-radius:5px;background:transparent;color:var(--text);text-align:left;padding:6px 9px;cursor:pointer}
.channel-overflow-menu button:hover,.channel-overflow-menu button:focus-visible{background:var(--hover)}
.composer-menu{position:relative}
.composer-menu>summary{list-style:none}
.composer-menu>summary::-webkit-details-marker{display:none}
.composer-popover{position:absolute;z-index:8;left:0;bottom:34px;min-width:210px;max-width:min(320px,80vw);max-height:220px;overflow:auto;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:6px}
.composer-popover button{display:flex;width:100%;gap:8px;align-items:center;border:0;border-radius:5px;background:transparent;color:var(--text);padding:7px 9px;text-align:left;cursor:pointer}
.composer-popover button span,.mention-suggestions button span{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.composer-popover button small,.mention-suggestions button small{margin-left:auto;color:var(--muted);white-space:nowrap}
.composer-popover button:hover,.composer-popover button:focus-visible,.composer-popover button[aria-selected="true"]{background:var(--panel)}
.emoji-grid{display:grid;grid-template-columns:repeat(6,36px);min-width:auto}
.emoji-grid button{justify-content:center;font-size:18px;padding:5px}
.mention-suggestions,.channel-suggestions,.emoji-suggestions,.slash-suggestions{position:absolute;z-index:9;left:8px;bottom:42px;min-width:220px;max-width:min(440px,85vw);max-height:240px;overflow:auto;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow);padding:6px}
.mention-suggestions button,.channel-suggestions button,.emoji-suggestions button,.slash-suggestions button{display:flex;width:100%;gap:8px;align-items:center;border:0;border-radius:5px;background:transparent;color:var(--text);padding:7px 9px;text-align:left;cursor:pointer}
.slash-suggestions button{align-items:flex-start;gap:10px}.slash-suggestions strong{min-width:100px}.slash-suggestions small{display:block;color:var(--muted)}
.mention-suggestions button:hover,.mention-suggestions button:focus-visible,.mention-suggestions button[aria-selected="true"],.channel-suggestions button:hover,.channel-suggestions button:focus-visible,.channel-suggestions button[aria-selected="true"],.emoji-suggestions button:hover,.emoji-suggestions button:focus-visible,.emoji-suggestions button[aria-selected="true"],.slash-suggestions button:hover,.slash-suggestions button:focus-visible,.slash-suggestions button[aria-selected="true"]{background:var(--panel)}
.emoji-glyph,.custom-emoji{display:inline-block;width:20px;height:20px;object-fit:contain;vertical-align:-4px}.emoji-glyph,.standard-emoji{font-size:18px;line-height:20px;text-align:center}.reaction-emoji{display:inline-grid;min-width:20px;place-items:center}.reaction-picker-form{display:none}
.emoji-picker-dialog{width:min(620px,calc(100vw - 28px));height:min(620px,calc(100vh - 28px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:0}.emoji-picker-dialog::backdrop{background:#0008}.emoji-picker-head{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;padding:14px;border-bottom:1px solid var(--line)}.emoji-picker-head label{display:grid;gap:5px;font-weight:800}.emoji-picker-head input{width:100%;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:9px 11px}.emoji-picker-close{align-self:end;border:0;background:transparent;color:var(--muted);font-size:22px}.emoji-picker-filters{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,180px);gap:8px;padding:10px 14px 0}.emoji-picker-filters label{display:grid;gap:4px;color:var(--muted);font-size:12px;font-weight:700}.emoji-picker-filters select{min-width:0;border:1px solid var(--field-line);border-radius:6px;background:var(--panel);color:var(--text);padding:7px}.emoji-picker-status{margin:0;padding:9px 14px;color:var(--muted);font-size:12px}.emoji-picker-results{display:grid;grid-template-columns:repeat(auto-fill,minmax(118px,1fr));gap:4px;margin:0;padding:0 10px 14px;list-style:none;overflow:auto;max-height:calc(100% - 174px)}.emoji-picker-results button{display:flex;width:100%;gap:7px;align-items:center;border:0;border-radius:6px;background:transparent;color:var(--text);padding:8px;text-align:left}.emoji-picker-results button:hover,.emoji-picker-results button:focus-visible,.emoji-picker-results button[aria-selected="true"]{background:var(--hover)}.emoji-picker-results small{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.shortcut-browser{width:min(620px,calc(100vw - 28px));height:min(620px,calc(100vh - 28px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:0}.shortcut-browser::backdrop{background:#0008}.shortcut-browser-head{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;padding:14px;border-bottom:1px solid var(--line)}.shortcut-browser-head label{display:grid;gap:5px;font-weight:800}.shortcut-browser-head input{width:100%;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:9px 11px}.shortcut-browser-head button{align-self:end;border:0;background:transparent;color:var(--muted);font-size:22px}.shortcut-browser-results{display:grid;gap:4px;padding:10px;overflow:auto;max-height:calc(100% - 78px)}.shortcut-browser-results>button,.shortcut-browser-results form>button{display:grid;grid-template-columns:minmax(110px,auto) minmax(0,1fr);align-items:start;gap:12px;width:100%;border:0;border-radius:7px;background:transparent;color:var(--text);padding:10px;text-align:left}.shortcut-browser-results>button:hover,.shortcut-browser-results>button:focus-visible,.shortcut-browser-results form>button:hover,.shortcut-browser-results form>button:focus-visible{background:var(--hover)}.shortcut-browser-results span{display:grid;gap:2px}.shortcut-browser-results small{color:var(--muted)}.shortcut-browser-empty{padding:30px;text-align:center;color:var(--muted)}
.clip-recorder{width:min(560px,calc(100vw - 28px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:18px}.clip-recorder::backdrop{background:#0008}.clip-recorder h2{margin:0 0 6px;font-size:18px}.clip-recorder p{margin:0 0 14px;color:var(--muted)}.clip-recorder video{display:block;width:100%;max-height:min(360px,55vh);margin:0 0 14px;border-radius:9px;background:#111;object-fit:contain}.clip-recorder-actions{display:flex;justify-content:flex-end;gap:8px}.clip-recorder-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel);color:var(--text);padding:8px 12px;font-weight:800}.clip-recorder-actions .clip-stop{border-color:var(--danger);background:var(--danger);color:var(--on-strong)}
.conversation-switcher{width:min(560px,calc(100vw - 32px));max-height:min(620px,calc(100vh - 32px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:0}
.conversation-switcher::backdrop{background:#0008}.switcher-head{display:flex;align-items:center;gap:10px;padding:14px;border-bottom:1px solid var(--line)}.switcher-head label{flex:1}.switcher-head input{width:100%;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:9px 11px}.switcher-close{border:0;background:transparent;color:var(--muted);font-size:20px}.switcher-results{list-style:none;margin:0;padding:8px;overflow:auto}.switcher-results a{display:flex;gap:8px;border-radius:6px;color:var(--text);padding:8px 10px;text-decoration:none}.switcher-results a:hover,.switcher-results a:focus-visible{background:var(--hover)}
.upload-preview{display:flex;align-items:center;gap:6px;flex-wrap:wrap;margin:5px 0 0;color:var(--muted);font-size:13px}.staged-file{display:inline-flex;align-items:center;gap:4px;padding:3px 6px;border:1px solid var(--line);border-radius:5px;background:var(--panel)}.staged-file button{padding:1px 4px}
.composer-footer{display:flex;justify-content:flex-end;align-items:center;gap:10px;flex-wrap:wrap}
.composer-tools{margin:0;color:var(--muted);font-size:13px}
.send{border:0;border-radius:5px;background:var(--ok);color:var(--on-strong);font-weight:700;padding:7px 14px}
.send-actions{display:flex;align-items:stretch;gap:2px}.schedule-menu{position:relative}.schedule-menu>summary{display:grid;place-items:center;height:100%;min-width:34px;border-radius:5px;background:var(--ok);color:var(--on-strong);cursor:pointer;list-style:none;font-weight:800}.schedule-menu>summary::-webkit-details-marker{display:none}.schedule-popover{position:absolute;z-index:10;right:0;bottom:38px;display:grid;gap:8px;width:min(310px,calc(100vw - 32px));padding:12px;border:1px solid var(--line);border-radius:9px;background:var(--panel-strong);box-shadow:var(--shadow)}.schedule-popover label{display:grid;gap:5px;font-size:12px;font-weight:800}.schedule-popover input{width:100%;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:8px 9px;font:inherit}.schedule-popover p{margin:0;color:var(--muted);font-size:12px}.schedule-popover button{border:0;border-radius:6px;background:var(--ok);color:var(--on-strong);padding:8px 11px;font-weight:800}.schedule-popover a{color:var(--action);font-size:12px;font-weight:700}
.thread{grid-area:thread;min-height:0;border-left:1px solid var(--line);background:var(--panel);padding:16px 18px;overflow:auto}
.thread h2{margin:0;font-size:16px}.thread-heading{display:flex;align-items:center;justify-content:space-between;gap:10px;margin:0 0 12px}.thread-heading form{margin:0}.thread-heading button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:6px 9px;font-weight:700}
@media(max-width:800px){
.workspace{grid-template-columns:minmax(0,1fr)}
.search{max-width:none}
.topbar{padding:0 8px;gap:8px}
.timeline,.composer-wrap,.channel-header{padding-left:12px;padding-right:12px}
{{if .ThreadTimestamp}}.content{grid-template-columns:minmax(0,1fr);grid-template-areas:"head" "thread" "composer"}
.timeline-wrap{display:none}
.thread{border-left:0;border-top:1px solid var(--line)}{{end}}
}
</style>`

const workspaceRefinements = `<style>
.topbar{height:44px;padding:0 10px;border-bottom:0;box-shadow:none}
.brand{max-width:220px}
.search{height:34px;max-width:680px;padding:3px 8px;background:#ffffff20}
.search-icon{width:16px;height:16px;flex:0 0 auto}
.keyboard-help{width:min(720px,calc(100vw - 28px));max-height:min(680px,calc(100vh - 28px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow);padding:0}.keyboard-help::backdrop{background:#0008}
.keyboard-help-head{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,220px) auto;align-items:center;gap:10px;padding:14px 16px;border-bottom:1px solid var(--line)}.keyboard-help-head h2{margin:0;font-size:1.1rem}.keyboard-help-head input{width:100%;border:1px solid var(--field-line);border-radius:7px;background:var(--panel);color:var(--text);padding:8px 10px}.keyboard-help-head button{border:0;background:transparent;color:var(--muted);font-size:22px;line-height:1}
.keyboard-help-body{padding:6px 16px 16px;overflow:auto;max-height:calc(min(680px,100vh - 28px) - 74px)}.keyboard-help-body h3{margin:16px 0 6px;font-size:.82rem;text-transform:uppercase;letter-spacing:.06em;color:var(--muted)}.keyboard-help-body dl{display:grid;gap:2px;margin:0}
.keyboard-help-body dl>div{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:baseline;gap:14px;padding:7px 8px;border-radius:6px}
.keyboard-help-body dl>div[hidden],.keyboard-help-body section[hidden]{display:none}.keyboard-help-body dl>div:nth-child(odd){background:var(--hover)}.keyboard-help-body dt{margin:0;min-width:0}.keyboard-help-body dt small{display:block;color:var(--muted);font-size:.78rem;font-weight:400}.keyboard-help-body dd{margin:0;display:flex;gap:6px;white-space:nowrap}
.keyboard-help-body kbd{border:1px solid var(--field-line);border-bottom-width:2px;border-radius:5px;background:var(--panel);padding:2px 6px;font:600 12px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace}
.keyboard-help-empty{padding:26px;text-align:center;color:var(--muted)}
.call-card{display:grid;gap:6px;margin:4px 0;padding:12px 14px;border:1px solid var(--line);border-radius:9px;background:var(--panel)}
.call-title{margin:0;font-weight:800}
.call-state{margin:0;color:var(--muted);font-size:12px}
.call-participants{display:flex;flex-wrap:wrap;gap:8px;margin:0;padding:0;list-style:none;color:var(--muted);font-size:12px}
.call-join{justify-self:start;display:inline-flex;align-items:center;min-height:32px;padding:0 12px;border-radius:6px;background:var(--action);color:var(--on-strong);font-weight:800;text-decoration:none}
.call-unavailable{margin:0;color:var(--muted)}
.assistant-state{display:grid;gap:8px;padding:12px 14px;border-bottom:1px solid var(--line);background:var(--panel)}
.assistant-title{margin:0;font-weight:800}
.assistant-status{margin:0;color:var(--muted);font-size:13px;font-style:italic}
.assistant-prompts{display:grid;gap:6px}.assistant-prompts-title{margin:0;color:var(--muted);font-size:12px;font-weight:700}
.assistant-prompts form{margin:0}
.assistant-prompt{display:block;width:100%;min-height:32px;padding:7px 10px;border:1px solid var(--field-line);border-radius:7px;background:var(--panel-strong);color:var(--text);text-align:left;font-weight:600}
.assistant-prompt:hover{background:var(--hover)}
.search-shortcut{border:1px solid #ffffff66;border-radius:4px;padding:0 5px;color:#fff;font-size:11px;line-height:20px;background:#0000001f}
.top-profile{display:grid;place-items:center;width:30px;height:30px;padding:0;border-radius:7px;background:#ffffff35;font-weight:800;text-transform:uppercase}
.workspace{grid-template-columns:260px minmax(0,1fr)}
.sidebar{padding:8px 0 16px;background:var(--chrome)}
.workspace-name{font-size:17px;line-height:1.25}
.workspace-sub{display:flex;align-items:center;gap:6px}
.presence-dot{display:inline-block;width:8px;height:8px;border-radius:50%;background:#2eb67d;box-shadow:0 0 0 2px #ffffff2e}
.side-link{min-height:28px}
.side-link[aria-current=page]{background:#1264a3;color:#fff}
.content{background:var(--panel-strong)}
.channel-header{min-height:52px;padding:8px 20px;background:var(--panel-strong)}
.channel-identity{display:flex;align-items:center;gap:10px;min-width:0}
.channel-title{display:flex;align-items:center;gap:4px;white-space:nowrap}
.channel-copy{min-width:0}
.channel-meta{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:680px}
.membership-pill{display:inline-flex;align-items:center;border:1px solid var(--line);border-radius:999px;padding:2px 8px;color:var(--muted);font-size:12px;font-weight:700;white-space:nowrap}
.membership-pill.joined{color:var(--ok);border-color:color-mix(in srgb,var(--ok) 45%,var(--line))}
/* Quiet controls. Slack's channel header carries the conversation — its name,
   its topic, who is in it — and keeps its controls light so they do not compete
   with it. These were bordered, bold and filled, which made five of them read
   as the most important thing on the row. */
.channel-actions button{border:1px solid transparent;border-radius:6px;background:transparent;color:var(--muted);padding:4px 8px;font-weight:600;font-size:13px}
.channel-actions button:hover{border-color:var(--line);color:var(--text)}
/* A quiet line under the channel header rather than a banner across the top of
   the conversation. The sentence stays: HUDDLE-01 requires the control to say
   what pressing it does, and a control that silently connected nothing is the
   promise the universal contract forbids. What changes is its weight — it is
   secondary information, not a panel competing with the first message. */
.huddle-bar{display:flex;flex-wrap:wrap;align-items:center;gap:6px 12px;padding:5px 26px;border-bottom:1px solid var(--line);background:transparent;color:var(--muted);font-size:12px}
.huddle-bar .huddle-media{color:var(--muted)}
.huddle-bar button{min-height:26px;padding:3px 10px;font-size:12px}
.add-column{margin:0 0 14px}
.add-column form{display:flex;flex-direction:column;gap:6px;max-width:420px;margin-top:8px}
.list-columns{margin:0 0 10px;font-size:12px}
.cells{display:flex;flex-wrap:wrap;gap:12px;flex:1}
.cell{display:flex;flex-direction:column}
.cell-name{font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:.03em}
.cell-value{font-size:14px}
.canvas-comments{margin-top:24px;border-top:1px solid var(--line);padding-top:16px}
.comments{list-style:none;margin:0 0 16px;padding:0;display:flex;flex-direction:column;gap:10px}
.comment{border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.comment-head{display:flex;flex-wrap:wrap;gap:8px;align-items:baseline}
.comment-author{font-weight:700}
.comment-anchor,.comment-time{color:var(--muted);font-size:12px}
.comment-text{margin:6px 0 8px;white-space:pre-wrap}
.new-comment{display:flex;flex-direction:column;gap:6px;max-width:520px}
.canvas-history{margin-top:24px;border-top:1px solid var(--line);padding-top:16px}
.revisions{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:12px}
.revision{border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.revision-head{display:flex;flex-wrap:wrap;gap:8px;align-items:baseline}
.revision-title{font-weight:700}
.revision-time,.revision-editor{color:var(--muted);font-size:12px}
.revision-excerpt{margin:6px 0 8px;color:var(--muted);font-size:13px}
.message-file.is-image{flex-wrap:wrap}
.message-image{max-width:min(360px,100%);max-height:280px;height:auto;border-radius:8px;display:block}
.message-file-meta.undescribed{color:var(--danger)}
.message.is-arrival{background:var(--mark-bg);border-radius:6px;transition:background 600ms ease-out}
@media (prefers-reduced-motion:reduce){.message.is-arrival{transition:none}}
.result mark,.message-text mark{background:var(--mark-bg);color:inherit;font-weight:700;border-radius:2px;padding:0 1px}
.typing{margin:0;padding:0 16px;min-height:18px;font-size:12px;color:var(--muted);display:flex;align-items:center;gap:6px}
.typing-dots{display:inline-flex;gap:3px}
.typing-dots i{width:4px;height:4px;border-radius:50%;background:currentColor;animation:typing-pulse 1.2s infinite ease-in-out}
.typing-dots i:nth-child(2){animation-delay:.2s}
.typing-dots i:nth-child(3){animation-delay:.4s}
@keyframes typing-pulse{0%,60%,100%{opacity:.25}30%{opacity:1}}
@media (prefers-reduced-motion:reduce){.typing-dots i{animation:none;opacity:.6}}
.huddle-bar.active{background:var(--hover)}
.huddle-state{display:grid;gap:2px;min-width:0}
.huddle-people{color:var(--text)}
.huddle-media{color:var(--muted)}
.huddle-actions{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin-left:auto}
.huddle-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:5px 9px;font-weight:700}
.huddle-actions button:hover{background:var(--hover)}
.huddle-actions .huddle-end{color:var(--danger);border-color:var(--danger)}
.channel-actions button:hover{background:var(--hover)}
.channel-actions a{color:var(--muted);text-decoration:none;font-weight:600;font-size:13px}
.channel-actions a:hover{color:var(--text);text-decoration:underline}
.timeline{padding-top:12px}
.message{border-radius:6px;padding-top:5px;padding-bottom:5px}
.message-actions{position:absolute;z-index:3;top:2px;right:10px;display:none;flex-wrap:nowrap;align-items:center;min-height:0;gap:2px;padding:3px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);box-shadow:var(--shadow)}
.message-action{display:inline-flex;align-items:center;justify-content:center;width:30px;height:30px;padding:0;border:0;border-radius:6px;background:transparent;color:var(--muted);cursor:pointer;list-style:none}
.message-action::-webkit-details-marker{display:none}
.message-action:hover{background:var(--hover);color:var(--text)}
.action-icon{width:18px;height:18px;display:block}
.message-more>.shortcut-list,.forward-menu>form{position:absolute;z-index:6;right:0;top:32px}
.message-actions a,.message-actions button,.message-actions summary{display:inline-flex;flex:0 0 auto;align-items:center;min-height:28px;border-radius:4px;padding:4px 7px;color:var(--muted);font-size:12px;font-weight:700;white-space:nowrap}
/* The toolbar is revealed by pointing at the message or focusing it, and is
   otherwise out of the way. It used to be permanently visible under every
   message, tripling the height of the timeline, because the rule above read
   ".message:hover .message-actions a, .message-actions button, .message-actions
   summary" — a comma-separated list, so the :hover prefix bound to the anchors
   alone and every button and menu stayed on screen. Two media queries below
   restore a visible state that nothing had ever hidden, which is the other half
   of the same mistake.
   Hidden with visibility, not opacity. Opacity was tried and fails WCAG: axe
   blends a zero-opacity foreground with its background rather than treating the
   text as absent, so every action reported a contrast of 1.42 against white.
   visibility:hidden takes the row out of the accessibility tree and out of the
   tab order, which is what "not currently offered" should mean, and :focus
   brings it back the moment the message is reached by keyboard — the message
   carries tabindex so roving focus lands on it. */
.message-actions{display:none}
.message:hover .message-actions,.message:focus .message-actions,.message:focus-within .message-actions{display:flex}
/* A menu left open keeps the toolbar on screen: closing it because the pointer
   moved into the menu it opened would make the menu unusable. */
.message-actions:has(details[open]){display:flex}
/* Overlaid at the top-right, the way Slack presents it, which needs the row to
   be narrow: as ten text labels it was nine hundred pixels wide and covered the
   message timestamp, which WCAG 2.2 target-size reports as a link obscured to a
   strip one pixel tall. Five icon controls clear the head with room to spare,
   and everything they displaced lives in More actions — which is also where
   Slack keeps it. */
.message-actions a:hover,.message-actions button:hover,.message-actions summary:hover{background:var(--hover);color:var(--text);text-decoration:none}
.message-actions details{display:inline-block;position:relative}
.message-actions summary{color:var(--muted);font-size:12px;cursor:pointer;list-style:none}
.message-actions summary::-webkit-details-marker{display:none}
.message-actions details[open]>form{position:absolute;z-index:5;right:0;top:34px;display:flex;width:max-content;max-width:min(480px,80vw);padding:9px;border:1px solid var(--line);border-radius:7px;background:var(--panel-strong);box-shadow:var(--shadow)}
.shortcut-list{display:grid;gap:3px;min-width:220px;padding:6px}
.message-actions details[open]>.shortcut-list{position:absolute;z-index:5;right:0;top:34px;width:max-content;max-width:min(360px,80vw);border:1px solid var(--line);border-radius:7px;background:var(--panel-strong);box-shadow:var(--shadow)}
.shortcut-list form{display:block}
.shortcut-list button{display:block;width:100%;padding:7px 9px;text-align:left}
.shortcut-list small{display:block;color:var(--muted);font-weight:400}
.composer-shortcuts{position:relative}
/* Attaching is a plus button at the head of the composer's own toolbar, where
   Slack puts it.
   
   It was a disclosure sitting above the composer, and the reason given was that
   the upload carries its own multipart form and HTML forbids a form inside a
   form. That reason was about the FORM and not about the control: the form was
   already never submitted natively when script is on, because
   stageSelectedFiles builds FormData from it and fetches. So the form stays
   where it is as the no-script fallback, and the button that opens it moved to
   where it belongs, with the disclosure hidden once script takes over. Nothing
   changed about how an upload is posted.
   
   The button is hidden until script reveals it, so a reader without script is
   never offered a control that cannot work. */
.composer-shortcuts summary{display:inline-flex;align-items:center;gap:6px;min-height:28px;width:max-content;cursor:pointer;padding:2px 8px;border-radius:6px;color:var(--muted);font-size:13px;font-weight:700}
.composer-shortcuts summary:hover{background:var(--hover);color:var(--text)}
.composer-shortcuts[open]>.shortcut-list{position:absolute;z-index:6;left:0;bottom:30px;border:1px solid var(--line);border-radius:7px;background:var(--panel-strong);box-shadow:var(--shadow)}
.message-actions .edit-message{width:min(420px,70vw)}
.message-actions .edit-message textarea{width:min(320px,55vw);min-height:64px;resize:vertical;border:1px solid var(--field-line);border-radius:4px;background:var(--panel-strong);color:var(--text);padding:5px 7px}
.message-actions .delete-message button{color:var(--danger);font-weight:700}
.new-channel{margin:4px 8px 0}
.new-channel summary{cursor:pointer;color:#f5eaf6;font-size:13px;font-weight:700;list-style:none;padding:5px 2px}
.new-channel summary::-webkit-details-marker{display:none}
.new-channel[open]{padding:8px;border:1px solid #ffffff4a;border-radius:6px;background:#0000001c}
.new-channel[open] summary{margin-bottom:7px}
.new-channel label{display:grid;gap:4px;color:#f5eaf6;font-size:12px;margin:6px 0}
.new-channel input[type=text]{min-width:0;width:100%;border:1px solid #ffffff8a;border-radius:4px;background:#ffffff1f;color:#fff;padding:6px 7px}
.new-channel .privacy{display:flex;grid-template-columns:none;align-items:center;gap:6px}
.new-channel button{width:100%;border:0;border-radius:5px;background:#fff;color:var(--accent);font-weight:800;padding:6px 9px}
.composer-wrap{background:var(--panel-strong);padding-top:7px;padding-bottom:12px}
.composer-shortcuts{margin:0 0 4px 2px}
.composer{border-color:var(--field-line);border-radius:9px;box-shadow:none;padding:8px 10px}
.composer:focus-within{border-color:var(--focus);box-shadow:0 0 0 1px var(--focus)}
.composer.is-dragging{border-color:var(--action);box-shadow:0 0 0 3px color-mix(in srgb,var(--action) 25%,transparent)}
.composer textarea{min-height:44px}
.composer-footer{border-top:1px solid var(--line);padding-top:7px;margin-top:6px}
.composer-tools kbd{border:1px solid var(--line);border-bottom-width:2px;border-radius:4px;padding:1px 5px;background:var(--panel);font:11px/1.4 inherit}
.send{min-width:70px}
.conversation-gate{border:1px solid var(--line);border-radius:9px;background:var(--panel);padding:14px 16px;display:flex;align-items:center;justify-content:space-between;gap:18px}
.conversation-gate-copy{min-width:0}
.conversation-gate strong{display:block;margin-bottom:2px}
.conversation-gate p{margin:0;color:var(--muted);font-size:13px}
.join-button{border:0;border-radius:6px;background:var(--ok);color:var(--on-strong);font-weight:800;padding:8px 18px;white-space:nowrap}
.conversation-details-backdrop{position:fixed;inset:0;z-index:18;display:grid;place-items:center;padding:24px;background:#0009}
.conversation-details{width:min(720px,calc(100vw - 32px));max-height:min(820px,calc(100vh - 32px));overflow:auto;border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);box-shadow:0 24px 80px #0007}
.conversation-details-head{position:sticky;top:0;z-index:1;display:flex;align-items:flex-start;gap:16px;padding:18px 20px;border-bottom:1px solid var(--line);background:var(--panel-strong)}
.conversation-details-head h2{margin:0;font-size:21px}.conversation-details-head p{margin:3px 0 0;color:var(--muted);font-size:13px}
.conversation-details-close{margin-left:auto;border-radius:6px;padding:5px 9px;color:var(--muted);font-size:22px;line-height:1;text-decoration:none}.conversation-details-close:hover{background:var(--hover);color:var(--text)}
.conversation-details-body{display:grid;gap:20px;padding:18px 20px}
.conversation-details-tabs{display:flex;gap:16px;border-bottom:1px solid var(--line);padding-bottom:10px}.conversation-details-tabs a{color:var(--text);font-weight:800;text-decoration:none}.conversation-details-tabs a:hover{text-decoration:underline}
.conversation-details-section{display:grid;gap:10px}.conversation-details-section h3{margin:0;font-size:15px}
.conversation-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin:0}
.conversation-facts div{padding:10px 12px;border:1px solid var(--line);border-radius:7px;background:var(--panel)}.conversation-facts dt{color:var(--muted);font-size:12px;font-weight:700}.conversation-facts dd{margin:3px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}
.conversation-settings{display:grid;gap:10px}.conversation-setting{display:grid;grid-template-columns:minmax(0,1fr) auto;align-items:end;gap:9px}.conversation-setting label{display:grid;gap:4px;font-size:12px;font-weight:700}
.conversation-setting input,.conversation-setting textarea,.conversation-setting select{width:100%;min-width:0;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:8px 9px;font:inherit}.conversation-setting textarea{min-height:70px;resize:vertical}
.conversation-setting button,.conversation-danger button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:8px 12px;font-weight:800}.conversation-setting button:hover{background:var(--hover)}
.conversation-members{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:7px;margin:0;padding:0;list-style:none}.conversation-member{display:flex;align-items:center;gap:8px;padding:7px 9px;border:1px solid var(--line);border-radius:7px}.conversation-member-avatar{width:26px;height:26px;display:grid;place-items:center;border-radius:5px;background:var(--hover);font-size:11px;font-weight:800}.conversation-member-name{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.conversation-member-status{display:inline-flex;align-items:center}.conversation-member-status .standard-emoji,.conversation-member-status .custom-emoji{width:16px;height:16px;font-size:15px;line-height:16px}
.conversation-danger{display:flex;flex-wrap:wrap;gap:8px;padding-top:16px;border-top:1px solid var(--line)}.conversation-danger form{display:inline-flex}.conversation-danger button{color:var(--danger);border-color:color-mix(in srgb,var(--danger) 50%,var(--line))}
.conversation-details-note{margin:0;color:var(--muted);font-size:13px}
.modal-backdrop{position:fixed;inset:0;z-index:20;display:grid;place-items:center;padding:24px;background:#0009}
.app-modal{display:grid;grid-template-rows:auto minmax(0,1fr) auto;width:min(620px,calc(100vw - 32px));max-height:min(760px,calc(100vh - 32px));border:1px solid var(--line);border-radius:12px;background:var(--panel-strong);box-shadow:0 24px 80px #0007;overflow:hidden}
.modal-head,.modal-foot{display:flex;align-items:center;gap:12px;padding:16px 20px}
.modal-head{border-bottom:1px solid var(--line)}.modal-head h2{margin:0;font-size:20px}.modal-app{color:var(--muted);font-size:12px}
.modal-close-x{margin-left:auto;border:0;border-radius:6px;background:transparent;color:var(--muted);font-size:24px;line-height:1;padding:4px 8px}.modal-close-x:hover{background:var(--hover);color:var(--text)}
.modal-form{display:contents}.modal-body{overflow:auto;padding:18px 20px}.modal-block{margin:0 0 16px}.modal-block.header{font-size:18px;font-weight:800}.modal-block.context{color:var(--muted);font-size:12px}.modal-block.divider{border:0;border-top:1px solid var(--line)}
.modal-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.modal-actions .block-action-options{width:100%}.modal-actions textarea{min-height:72px}
.modal-input{display:grid;gap:6px}.modal-input>label,.modal-legend{font-weight:700}.modal-input input:not([type=radio]):not([type=checkbox]),.modal-input textarea,.modal-input select{width:100%;border:1px solid var(--field-line);border-radius:6px;background:var(--bg);color:var(--text);padding:9px 10px;font:inherit}.modal-input textarea{min-height:96px;resize:vertical}.modal-input select[multiple]{min-height:120px}
.modal-hint{margin:0;color:var(--muted);font-size:12px}.modal-error{margin:0;color:var(--danger);font-size:13px;font-weight:700}.modal-options{display:grid;gap:7px;margin:0;padding:0;border:0}.modal-options label{display:flex;align-items:flex-start;gap:7px}
.modal-foot{justify-content:flex-end;border-top:1px solid var(--line)}.modal-button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:8px 16px;font-weight:800}.modal-button.primary{border-color:var(--ok);background:var(--ok);color:var(--on-strong)}.modal-button:disabled{opacity:.55}
/* Timeline styling that belongs at every width. This run lived inside the
   narrow-viewport media query below, so on a desktop the timeline had no date
   separator, no unread divider, no system-message treatment, no edited label
   and no thread summary — the rules were served and never applied. A date
   divider rendered as bare left-aligned text because .day-separator was
   mobile-only. */
.day-separator{display:flex;align-items:center;gap:8px;margin:14px 0 6px;color:var(--muted);font-size:12px}
.day-separator::before,.day-separator::after{content:"";flex:1;height:1px;background:var(--field-line)}
.unread-divider{display:flex;align-items:center;gap:8px;margin:10px 0;color:var(--action);font-size:12px;font-weight:700}
.unread-divider::before,.unread-divider::after{content:"";flex:1;height:1px;background:var(--action)}
/* No blanket opacity. The rule that used to be here faded the whole message to
   85%, which was harmless only because it had been stranded in a media query
   and never applied; switched on, it blends the muted text toward whatever is
   behind it and drops a system line to 4.35:1 against a highlighted row — under
   the 4.5:1 AA needs. A system message is secondary because .system-text is
   muted, which is a colour decision that survives being read on any background,
   not a transparency that quietly degrades every one of them. */
.system-text{margin:0;color:var(--muted);font-size:13px}
.edited-label,.broadcast-label{margin-left:6px;color:var(--muted);font-size:11px}
.thread-summary{margin:2px 0 0;font-size:12px}
.thread-summary a{color:var(--action);font-weight:700;text-decoration:none}
.thread-last-reply{color:var(--muted);margin-left:6px}
.file-delete summary{cursor:pointer;color:var(--muted);font-size:12px}
.file-delete p{margin:6px 0;font-size:12px;color:var(--muted)}
/* A system message carries no avatar, so its body landed in the 38px avatar
   column of .message's two-column grid and wrapped one word per line. Putting
   it in the text column keeps the gutter empty, which is where Slack aligns
   these lines too. */
.system-message>.message-body{grid-column:2}
@media(max-width:800px){
.brand{display:none}
html.js .nav-toggle{display:grid;place-items:center;flex:0 0 auto;width:34px;height:34px;border:1px solid #ffffff8a;border-radius:6px;background:transparent;color:var(--on-accent);font-size:21px;line-height:1}
html.js .nav-toggle:hover{background:#ffffff2b}
html.js .nav-scrim{position:fixed;inset:48px 0 0;z-index:7;border:0;background:#0008}
html.js .nav-scrim.is-open{display:block}
.workspace{grid-template-columns:minmax(0,1fr)}
html.js .sidebar{position:fixed;inset:48px auto 0 0;z-index:8;width:min(320px,calc(100vw - 48px));padding:12px 8px;transform:translateX(-105%);transition:transform .18s ease;box-shadow:var(--shadow)}
html.js .sidebar.is-open{transform:translateX(0)}
.workspace-name{display:block}
.workspace-sub{display:flex}
.side-label,.side-text,.signed-in-name{display:block}
.side-link{justify-content:flex-start;padding:7px 10px}
.side-more{padding:6px 10px;text-align:left;font-size:13px}
.side-icon{font-size:inherit}
.search-shortcut{display:none}
.search-submit{display:none}
.channel-header{padding-left:12px;padding-right:12px}
.membership-pill{display:none}
.message-actions{position:static;margin-top:2px;padding:0;border:0;background:transparent;box-shadow:none;opacity:1;visibility:visible}
.message-actions details[open]>form{left:0;right:auto}
.conversation-gate{align-items:stretch;flex-direction:column}
.join-button{width:100%}
.new-channel{position:relative;margin-left:4px;margin-right:4px}
.conversation-details-backdrop{padding:0;align-items:end}.conversation-details{width:100%;max-height:calc(100vh - 48px);border-radius:12px 12px 0 0}.conversation-facts{grid-template-columns:minmax(0,1fr)}.conversation-setting{grid-template-columns:minmax(0,1fr)}.conversation-setting button{width:100%}
.modal-backdrop{padding:0;align-items:end}.app-modal{width:100%;max-height:calc(100vh - 48px);border-radius:12px 12px 0 0}
}
@media(hover:none){
.message-actions{position:static;margin-top:2px;padding:0;border:0;background:transparent;box-shadow:none;opacity:1;visibility:visible}
}
</style>`

const attachmentPartial = `{{define "attachment"}}
<article class="message-attachment">
  {{if .Pretext}}<p class="pretext">{{.Pretext}}</p>{{end}}
  {{if .Author}}<div class="attachment-author">{{.Author}}</div>{{end}}
  {{if .Title}}{{if .TitleURL}}<a class="attachment-title" href="{{.TitleURL}}" rel="noreferrer noopener">{{.Title}}</a>{{else}}<strong class="attachment-title">{{.Title}}</strong>{{end}}{{end}}
  {{if .Text}}<p class="attachment-text">{{.Text}}</p>{{end}}
  {{if .Fields}}<ul class="attachment-fields">{{range .Fields}}<li>{{if .Title}}<strong>{{.Title}}</strong><br>{{end}}{{.Value}}</li>{{end}}</ul>{{end}}
  {{if .Blocks}}<div class="message-blocks">{{range $block := .Blocks}}{{if eq $block.Kind "divider"}}<hr class="message-block divider">{{else}}<div class="message-block {{$block.Kind}}">{{if $block.HTML}}<div class="formatted-text">{{$block.HTML}}</div>{{else}}{{$block.Text}}{{end}}{{if $block.Fields}}<ul class="message-block-fields">{{range $index, $field := $block.Fields}}<li>{{with index $block.FieldHTML $index}}{{.}}{{else}}{{$field}}{{end}}</li>{{end}}</ul>{{end}}{{if $block.Table}}<div class="block-table-wrap"><table class="block-table">{{if $block.Caption}}<caption>{{$block.Caption}}</caption>{{end}}<tbody>{{range $rowIndex, $row := $block.Table}}<tr>{{range $row}}{{if and $block.HeaderRow (eq $rowIndex 0)}}<th scope="col">{{.}}</th>{{else}}<td>{{.}}</td>{{end}}{{end}}</tr>{{end}}</tbody></table></div>{{end}}</div>{{end}}{{end}}</div>{{end}}
  {{if .ImageURL}}<img class="message-media" src="{{.ImageURL}}" alt="{{.ImageAlt}}" loading="lazy">{{end}}
  {{if .Footer}}<div class="attachment-footer">{{.Footer}}</div>{{end}}
  {{if .SourceURL}}<a class="unfurl-source" href="{{.SourceURL}}" rel="noreferrer noopener">{{.SourceURL}}</a>{{end}}
</article>
{{end}}`

const messagesPartial = `{{define "icon-emoji"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="10" cy="10" r="7.25" fill="none" stroke="currentColor" stroke-width="1.5"/><circle cx="7.4" cy="8.4" r="1" fill="currentColor"/><circle cx="12.6" cy="8.4" r="1" fill="currentColor"/><path d="M6.9 12.2a4 4 0 0 0 6.2 0" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>{{end}}
{{define "icon-thread"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M3.2 6.2A2 2 0 0 1 5.2 4.2h9.6a2 2 0 0 1 2 2v5.2a2 2 0 0 1-2 2H8.4L5 16.2v-2.8a2 2 0 0 1-1.8-2Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-forward"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M11 4.5 17 10l-6 5.5V12C7.6 12 5.2 13.1 3.5 15.5c.3-4.6 3-7.4 7.5-7.9Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-bookmark"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="M5.5 3.8h9v12.4L10 12.6l-4.5 3.6Z" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>{{end}}
{{define "icon-more"}}<svg class="action-icon" viewBox="0 0 20 20" aria-hidden="true" focusable="false"><circle cx="4.6" cy="10" r="1.4" fill="currentColor"/><circle cx="10" cy="10" r="1.4" fill="currentColor"/><circle cx="15.4" cy="10" r="1.4" fill="currentColor"/></svg>{{end}}
{{define "messages"}}
{{range $message := .Messages}}
{{if $message.DaySeparator}}<div class="day-separator" role="separator"><time datetime="{{$message.DaySeparatorMachine}}">{{$message.DaySeparator}}</time></div>{{end}}
{{if $message.FirstUnread}}<div class="unread-divider" role="separator" aria-label="New messages"><span>New</span></div>{{end}}
{{if $message.System}}<article class="message system-message" id="{{$message.Anchor}}" data-message-id="{{$message.ID}}" data-subtype="{{$message.Subtype}}" tabindex="-1" aria-label="{{$message.DisplayText}}">
  <div class="message-body"><p class="system-text">{{$message.DisplayText}}</p><time class="time" datetime="{{$message.MachineTime}}">{{$message.DisplayTime}}</time></div>
</article>
{{else}}
<article class="message" id="{{$message.Anchor}}" data-message-id="{{$message.ID}}" tabindex="-1" aria-label="{{if $message.Ephemeral}}Private message only visible to you{{else}}Message{{end}} from {{$message.AuthorName}} at {{$message.DisplayTime}}" aria-keyshortcuts="ArrowUp ArrowDown Home End ArrowRight T{{if not $message.Ephemeral}} A M F{{end}}{{if $message.MarkUnreadURL}} U{{end}}{{if $message.CanEdit}} E{{end}}{{if $.CanPin}} P{{end}}{{if $.CanReact}} R{{end}}{{if $message.CanDelete}} Delete{{end}}">
  <div class="avatar{{if $message.AvatarEmoji}} avatar-emoji{{end}}" aria-hidden="true">{{if $message.AvatarURL}}<img src="{{$message.AvatarURL}}" alt="">{{else if $message.AvatarEmoji}}{{$message.AvatarEmoji}}{{else}}{{$message.AuthorInitial}}{{end}}</div>
  <div class="message-body">
    <div class="message-head">
      <span class="author">{{$message.AuthorName}}</span>{{if $message.AuthorStatus}}<span class="author-status"{{if $message.AuthorStatusText}} title="{{$message.AuthorStatusText}}"{{end}}>{{$message.AuthorStatus}}</span>{{end}}{{if $message.IsApp}}<span class="app-label">APP</span>{{end}}
      {{if $message.Permalink}}<a class="time" href="{{$message.Permalink}}"><time datetime="{{$message.MachineTime}}">{{$message.DisplayTime}}</time></a>{{else}}<time class="time" datetime="{{$message.MachineTime}}">{{$message.DisplayTime}}</time>{{end}}{{if $message.Edited}}<span class="edited-label" title="Edited {{$message.EditedTime}}">(edited)</span>{{end}}{{if $message.Broadcast}}<span class="broadcast-label">Also sent to the channel</span>{{end}}{{if $message.Streaming}}<span class="streaming-label" role="status">Responding…</span>{{end}}
      {{if $message.Pinned}}<span class="pinned">Pinned</span>{{end}}
      {{if $message.Ephemeral}}<span class="ephemeral-label">Only visible to you</span>{{end}}
    </div>
    {{if $message.DisplayText}}<p class="message-text">{{$message.DisplayText}}</p>{{end}}
    {{if $message.Files}}<div class="message-files" aria-label="Shared files">{{range $file := $message.Files}}
      <div class="message-file{{if $file.IsImage}} is-image{{end}}">
        {{if $file.IsImage}}<a class="message-image-link" href="{{$file.DownloadURL}}"><img class="message-image" src="{{$file.DownloadURL}}" alt="{{$file.AccessibleName}}" loading="lazy"></a>
        {{else}}<span class="message-file-icon" aria-hidden="true">FILE</span>{{end}}
        <span class="message-file-copy"><span class="message-file-title">{{$file.Title}}</span><span class="message-file-meta">{{$file.Name}} · {{$file.MIMEType}} · {{$file.Size}}</span>{{if and $file.IsImage (not $file.AccessibleName)}}<span class="message-file-meta undescribed">No description yet</span>{{end}}</span>
        {{if $file.Deleted}}<span class="message-file-meta">Deleted</span>{{else}}<a href="{{$file.DownloadURL}}" download>Download</a>{{if $file.DeleteURL}}<details class="file-delete"><summary>Delete file</summary>
          <form method="post" action="{{$file.DeleteURL}}" hx-post="{{$file.DeleteURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <p>Deleting {{$file.Title}} removes it from every message and search result in this workspace. This cannot be undone.</p>
            <button type="submit">Delete this file</button>
          </form>
        </details>{{end}}{{if $file.DescribeURL}}<details class="file-describe"><summary>{{if $file.Description}}Edit description{{else}}Add a description{{end}}</summary>
          <form method="post" action="{{$file.DescribeURL}}" hx-post="{{$file.DescribeURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="describe-{{$file.ID}}">Describe this image for people who cannot see it</label>
            <textarea id="describe-{{$file.ID}}" name="description" maxlength="1000" rows="2">{{$file.Description}}</textarea>
            <button type="submit">Save description</button>
          </form>
        </details>{{end}}{{end}}
      </div>{{end}}</div>{{end}}
    {{if $message.Blocks}}
    <div class="message-blocks" aria-label="Structured message">
      {{range $block := $message.Blocks}}
        {{if eq $block.Kind "divider"}}<hr class="message-block divider">
        {{else}}<div class="message-block {{$block.Kind}}">
          {{if $block.HTML}}<div class="formatted-text">{{$block.HTML}}</div>{{else if $block.Text}}<div>{{$block.Text}}</div>{{end}}
          {{if $block.Fields}}<ul class="message-block-fields">{{range $index, $field := $block.Fields}}<li>{{with index $block.FieldHTML $index}}{{.}}{{else}}{{$field}}{{end}}</li>{{end}}</ul>{{end}}
          {{if $block.Table}}<div class="block-table-wrap"><table class="block-table">{{if $block.Caption}}<caption>{{$block.Caption}}</caption>{{end}}<tbody>{{range $rowIndex, $row := $block.Table}}<tr>{{range $cell := $row}}{{if and $block.HeaderRow (eq $rowIndex 0)}}<th scope="col">{{$cell}}</th>{{else}}<td>{{$cell}}</td>{{end}}{{end}}</tr>{{end}}</tbody></table></div>{{end}}
          {{if and $message.CanInteract $block.Actions}}<div class="message-block-actions" aria-label="Message actions">{{range $action := $block.Actions}}
            {{if $action.Dispatch}}<form method="post" action="/app/interaction" hx-post="/app/interaction">{{else}}<div>
              {{end}}
              <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
              <input type="hidden" name="message_id" value="{{$message.MessageID}}">
              <input type="hidden" name="app_id" value="{{$message.AppID}}">
              <input type="hidden" name="block_id" value="{{$action.BlockID}}">
              <input type="hidden" name="action_id" value="{{$action.ActionID}}">
              <input type="hidden" name="action_type" value="{{$action.Type}}">
              <input type="hidden" name="channel" value="{{$message.Channel}}">
              {{if eq $action.Control "button"}}<input type="hidden" name="value" value="{{$action.Value}}">{{if $action.Dispatch}}<button class="block-action{{if $action.Tone}} feedback-{{$action.Tone}}{{end}}" type="submit"{{if $action.AccessibilityLabel}} aria-label="{{$action.AccessibilityLabel}}"{{end}}>{{$action.Text}}</button>{{end}}
              {{else if eq $action.Control "date"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="date" name="value" value="{{$action.Value}}" required></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
              {{else if eq $action.Control "time"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="time" name="value" value="{{$action.Value}}" required></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
              {{else if eq $action.Control "datetime"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="datetime-local" name="value" data-unix-seconds="true" required></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
              {{else if eq $action.Control "radio"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="radio" name="value" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}} required> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
              {{else if eq $action.Control "checkbox"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="checkbox" name="value" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}
              {{else if eq $action.Control "external"}}<div class="external-select" data-app-options data-app-id="{{$message.AppID}}" data-message-id="{{$message.MessageID}}" data-block-id="{{$action.BlockID}}" data-action-id="{{$action.ActionID}}" data-channel="{{$message.Channel}}" data-min-query="{{$action.MinQueryLength}}"><label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="search" data-options-query placeholder="{{$action.Text}}" minlength="{{$action.MinQueryLength}}"></label><button class="block-action" type="button" data-options-load>Search</button><label><span class="sr-only">Results</span><select class="block-action block-action-select" name="value" data-options-results{{if $action.Multiple}} multiple{{end}}{{if not $action.Options}} disabled{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit" data-options-choose{{if not $action.Options}} disabled{{end}}>Choose</button>{{end}}<p class="external-select-status" data-options-status role="status"></p><noscript>Dynamic options require JavaScript in this client.</noscript></div>
              {{else if or (eq $action.Control "text") (eq $action.Control "textarea") (eq $action.Control "email") (eq $action.Control "url") (eq $action.Control "number")}}{{if eq $action.Control "textarea"}}<textarea class="block-action" name="value" placeholder="{{$action.Text}}">{{$action.Value}}</textarea>{{else}}<input class="block-action" type="{{$action.Control}}" name="value" value="{{$action.Value}}" placeholder="{{$action.Text}}">{{end}}{{if $action.Dispatch}}<button class="block-action" type="submit">Send</button>{{end}}
              {{else}}<label><span class="sr-only">{{$action.Text}}</span><select class="block-action block-action-select" name="value"{{if $action.Multiple}} multiple{{end}} required>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label>{{if $action.Dispatch}}<button class="block-action" type="submit">Choose</button>{{end}}{{end}}
            {{if $action.Dispatch}}</form>{{else}}</div>{{end}}
          {{end}}</div>{{end}}
          {{if $block.Call}}<div class="call-card">
            {{if $block.Call.Unavailable}}<p class="call-unavailable">This call is no longer available.</p>
            {{else}}<p class="call-title">{{if $block.Call.Title}}{{$block.Call.Title}}{{else}}Call{{end}}</p>
            <p class="call-state">{{if $block.Call.Active}}In progress{{else}}Ended{{end}}{{if $block.Call.Participants}} · {{len $block.Call.Participants}} in the call{{end}}</p>
            {{if $block.Call.Participants}}<ul class="call-participants">{{range $block.Call.Participants}}<li>{{.}}</li>{{end}}</ul>{{end}}
            {{if and $block.Call.Active $block.Call.JoinURL}}<a class="call-join" href="{{$block.Call.JoinURL}}" rel="noreferrer noopener">Join call</a>
            {{else if $block.Call.JoinURL}}<p class="call-state">This call has ended. The link it was created with is no longer offered.</p>{{end}}{{end}}
          </div>{{end}}
          {{if $block.ImageURL}}<img class="message-media" src="{{$block.ImageURL}}" alt="{{$block.ImageAlt}}" loading="lazy">{{end}}
          {{if $block.LinkURL}}<a href="{{$block.LinkURL}}" rel="noreferrer noopener">{{$block.LinkLabel}}</a>{{end}}
        </div>{{end}}
      {{end}}
    </div>
    {{end}}
    {{if $message.Attachments}}
    <div class="message-attachments" aria-label="Attachments">
      {{range $attachment := $message.Attachments}}{{template "attachment" $attachment}}{{end}}
    </div>
    {{end}}
    {{if $message.Unfurls}}
    <div class="message-unfurls" aria-label="Link previews">
      {{range $attachment := $message.Unfurls}}{{template "attachment" $attachment}}{{end}}
    </div>
    {{end}}
    {{if $message.Reactions}}
    <ul class="reactions">
      {{range $reaction := $message.Reactions}}
      <li>
        {{if $.CanReact}}
        <form class="inline-form" method="post" action="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}" hx-post="{{if $reaction.Mine}}{{$message.UnreactURL}}{{else}}{{$message.ReactionURL}}{{end}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <input type="hidden" name="name" value="{{$reaction.Name}}">
          <button class="chip" type="submit" aria-pressed="{{if $reaction.Mine}}true{{else}}false{{end}}" aria-label="{{if $reaction.Mine}}Remove your {{$reaction.Name}} reaction{{else}}React with {{$reaction.Name}}{{end}}, {{$reaction.Count}} so far"><span class="reaction-emoji">{{$reaction.Display}}</span> <span class="chip-count">{{$reaction.Count}}</span></button>
        </form>
        {{else}}
        <span class="chip" role="img" aria-label="{{$reaction.Name}}, {{$reaction.Count}} reactions"><span class="reaction-emoji">{{$reaction.Display}}</span> <span class="chip-count">{{$reaction.Count}}</span></span>
        {{end}}
      </li>
      {{end}}
    </ul>
    {{end}}
    {{if $message.ReplyCount}}<p class="thread-summary"><a href="{{$message.ReplyURL}}">{{$message.ReplySummary}}</a>{{if $message.LastReplyTime}} <span class="thread-last-reply">Last reply {{$message.LastReplyTime}}</span>{{end}}</p>{{end}}
    {{if not $message.Ephemeral}}<div class="message-actions" role="group" aria-label="Actions for the message from {{$message.AuthorName}}">
      {{if $.CanReact}}
      <form id="reaction-form-{{$message.ID}}" class="reaction-picker-form" aria-label="Add a reaction to the message from {{$message.AuthorName}}" method="post" action="{{$message.ReactionURL}}" hx-post="{{$message.ReactionURL}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <input type="hidden" name="name" value="">
      </form>
      <button class="message-action" type="button" data-open-emoji-picker data-emoji-target="reaction" data-reaction-form="reaction-form-{{$message.ID}}" aria-haspopup="dialog" aria-label="Add reaction" title="Add reaction">{{template "icon-emoji"}}</button>
      {{end}}
      <a class="message-action" href="{{$message.ReplyURL}}" aria-label="{{if $.CanReply}}Reply in thread{{else}}View thread{{end}}" title="{{if $.CanReply}}Reply in thread{{else}}View thread{{end}}">{{template "icon-thread"}}</a>
      {{if and $message.ForwardURL $.CanReply}}<details class="forward-menu"><summary class="message-action" aria-label="Forward" title="Forward">{{template "icon-forward"}}</summary>
        <form method="post" action="{{$message.ForwardURL}}" hx-post="{{$message.ForwardURL}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <label>Forward to<select name="destination">{{range $.ForwardDestinations}}<option value="{{.ID}}">#{{.Name}}</option>{{end}}</select></label>
          <label>Add a message<input name="comment" maxlength="2000"></label>
          <button type="submit">Forward</button>
        </form>
      </details>{{end}}
      <form method="post" action="{{if $message.Saved}}{{$message.UnsaveURL}}{{else}}{{$message.SaveURL}}{{end}}" hx-post="{{if $message.Saved}}{{$message.UnsaveURL}}{{else}}{{$message.SaveURL}}{{end}}" data-message-save>
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
        <button class="message-action" type="submit" aria-pressed="{{if $message.Saved}}true{{else}}false{{end}}" aria-label="{{if $message.Saved}}Remove from Later{{else}}Save for later{{end}}" title="{{if $message.Saved}}Remove from Later{{else}}Save for later{{end}}">{{template "icon-bookmark"}}</button>
      </form>
      <details class="message-more"><summary class="message-action" aria-label="More actions" title="More actions">{{template "icon-more"}}</summary>
      <div class="shortcut-list">
        {{if $message.CopyLinkURL}}<a class="copy-link" href="{{$message.CopyLinkURL}}" data-copy-link="{{$message.CopyLinkURL}}">Copy link</a>{{end}}
        {{if $message.MarkUnreadURL}}<form method="post" action="{{$message.MarkUnreadURL}}" hx-post="{{$message.MarkUnreadURL}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <button type="submit">Mark unread from here</button>
        </form>{{end}}
        <details data-reminder-menu>
          <summary>Remind me about this</summary>
          <form class="inline-form reminder-form" aria-label="Set a reminder for this message" method="post" action="{{$message.RemindURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <input type="hidden" name="timezone" data-browser-timezone value="UTC">
            <button type="submit" name="preset" value="20m">In 20 minutes</button>
            <button type="submit" name="preset" value="1h">In 1 hour</button>
            <button type="submit" name="preset" value="tomorrow">Tomorrow at 9:00 AM</button>
            <label>Custom date<input type="date" name="date"></label>
            <label>Time<input type="time" name="time" value="09:00"></label>
            <button type="submit" name="preset" value="custom">Set reminder</button>
          </form>
        </details>
        {{if $.CanPin}}
        <form method="post" action="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}" hx-post="{{if $message.Pinned}}{{$message.UnpinURL}}{{else}}{{$message.PinURL}}{{end}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <button type="submit">{{if $message.Pinned}}Unpin{{else}}Pin{{end}}</button>
        </form>
        {{end}}
        {{range $shortcut := $message.Shortcuts}}
        <form method="post" action="/app/shortcut" hx-post="/app/shortcut">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <input type="hidden" name="channel" value="{{$message.Channel}}">
          <input type="hidden" name="message_id" value="{{$message.MessageID}}">
          <input type="hidden" name="app_id" value="{{$shortcut.AppID}}">
          <input type="hidden" name="callback_id" value="{{$shortcut.CallbackID}}">
          <button type="submit">{{$shortcut.Name}}<small>{{$shortcut.AppName}} · {{$shortcut.Description}}</small></button>
        </form>
        {{end}}
        {{if $message.CanEdit}}
        <details>
          <summary>Edit</summary>
          <form class="inline-form edit-message" aria-label="Edit message" method="post" action="{{$message.UpdateURL}}" hx-post="{{$message.UpdateURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label class="visually-hidden" for="edit-{{$message.ID}}">Edit your message</label>
            <textarea id="edit-{{$message.ID}}" name="text" maxlength="40000" required>{{$message.Text}}</textarea>
            <button type="submit">Save changes</button>
          </form>
        </details>
        {{end}}
        {{if $message.CanDelete}}
        <details class="delete-message">
          <summary>Delete</summary>
          <form aria-label="Delete message" method="post" action="{{$message.DeleteURL}}" hx-post="{{$message.DeleteURL}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            {{if $message.Files}}<p>This message shares a file. Deleting it also removes the file from this conversation, unless another message here shares it too. The file itself is kept.</p>{{end}}
            <button type="submit">Delete this message</button>
          </form>
        </details>
        {{end}}
      </div>
      </details>
    </div>{{end}}
  </div>
</article>
{{end}}
{{else}}
<p class="empty">{{if .IsMember}}No messages yet. Start the conversation.{{else}}No messages have been posted in this channel yet.{{end}}</p>
{{end}}
{{end}}`

var pageMarkup = attachmentPartial + `{{define "title"}}{{.ChannelPrefix}}{{.ChannelName}} · {{.WorkspaceName}}{{end}}
{{define "styles"}}` + pageStyle + workspaceRefinements + `{{end}}
{{define "scripts"}}` + progressiveEnhancementScript + searchSuggestionsScript + appOptionsScript + huddleMediaScript + `{{end}}
{{define "content"}}
<a class="skip-link" href="#timeline">Skip to the messages</a>
<div class="shell" data-browser-notifications="{{if .BrowserNotifications}}true{{else}}false{{end}}" data-notifications-paused="{{if .NotificationsPaused}}true{{else}}false{{end}}" data-channel-name="{{.ChannelName}}">
  <header class="topbar">
    <button class="nav-toggle" id="nav-toggle" type="button" aria-controls="workspace-sidebar" aria-expanded="false" aria-label="Open navigation"><span aria-hidden="true">☰</span></button>
    <span class="brand">{{.WorkspaceName}}</span>
    <form class="search" method="get" action="/app/search" role="search" aria-label="Search {{.WorkspaceName}}">
      <svg class="search-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><circle cx="8.5" cy="8.5" r="5.5"/><path d="m13 13 4 4"/></svg>
      <label class="visually-hidden" for="workspace-search">Search {{.WorkspaceName}}</label>
      <input id="workspace-search" type="search" name="q" maxlength="500" placeholder="Search {{.WorkspaceName}}" role="combobox" {{ariaKeyshortcuts "Search the workspace"}} autocomplete="off" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded="false" required>
      <span class="search-shortcut" aria-hidden="true">⌘/Ctrl G</span>
      <button class="search-submit" type="submit">Search</button>
      <input type="hidden" name="channel" value="{{.Channel}}">
    </form>
    <div class="top-actions">
      <button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button>
      {{if .ShowProfile}}<a class="icon-button top-profile" href="/me" aria-label="My profile">{{.UserInitial}}</a>{{end}}
    </div>
  </header>
  <dialog class="conversation-switcher" id="conversation-switcher" aria-labelledby="conversation-switcher-title">
    <div class="switcher-head"><label><span class="visually-hidden" id="conversation-switcher-title">Jump to a conversation</span><input id="conversation-switcher-query" type="search" autocomplete="off" placeholder="Jump to a conversation"></label><button class="switcher-close" type="button" aria-label="Close conversation switcher">×</button></div>
    <ul class="switcher-results" id="conversation-switcher-results">{{range .Channels}}<li><a href="/app?channel={{.ID}}" data-conversation-name="{{.Name}}"><span aria-hidden="true">#</span><span>{{.Name}}</span></a></li>{{end}}{{range .Directs}}<li><a href="/app?channel={{.ID}}" data-conversation-name="{{.Name}}"><span aria-hidden="true">@</span><span>{{.Name}}</span></a></li>{{end}}</ul>
  </dialog>
  {{if or .CanPost .Timeline.CanReact}}<dialog class="emoji-picker-dialog" id="emoji-picker-dialog" aria-labelledby="emoji-picker-title">
    <div class="emoji-picker-head"><label><span id="emoji-picker-title">Emoji</span><input id="emoji-picker-query" type="search" autocomplete="off" maxlength="100" placeholder="Search emoji"></label><button class="emoji-picker-close" id="emoji-picker-close" type="button" aria-label="Close emoji picker">×</button></div>
    <div class="emoji-picker-filters"><label>Category<select id="emoji-picker-category"><option value="">All emoji</option><option value="Recent">Recent</option><option value="Custom">Custom</option></select></label><label>Skin tone<select id="emoji-picker-tone"><option value="">Default</option><option value="2">Light</option><option value="3">Medium-light</option><option value="4">Medium</option><option value="5">Medium-dark</option><option value="6">Dark</option></select></label></div>
    <p class="emoji-picker-status" id="emoji-picker-status" role="status">Choose an emoji.</p>
    <ul class="emoji-picker-results" id="emoji-picker-results" role="listbox" aria-label="Emoji results"></ul>
  </dialog>{{end}}
  {{if and .CanPost (or .SlashCommands .GlobalShortcuts)}}<dialog class="shortcut-browser" id="shortcut-browser" aria-labelledby="shortcut-browser-title">
    <div class="shortcut-browser-head"><label><span id="shortcut-browser-title">Shortcuts</span><input id="shortcut-browser-query" type="search" autocomplete="off" placeholder="Search shortcuts and commands"></label><button id="shortcut-browser-close" type="button" aria-label="Close shortcuts">×</button></div>
    <div class="shortcut-browser-results" id="shortcut-browser-results">{{range .SlashCommands}}<button type="button" data-browser-command="{{.Command}}" data-shortcut-search="{{.Command}} {{.Description}} {{.UsageHint}} {{.AppName}}"><strong>{{.Command}}</strong><span>{{.Description}}{{if .UsageHint}}<small>{{.UsageHint}}</small>{{end}}{{if .AppName}}<small>{{.AppName}}</small>{{end}}</span></button>{{end}}{{range $shortcut := .GlobalShortcuts}}
      <form method="post" action="/app/shortcut" hx-post="/app/shortcut" data-shortcut-search="{{$shortcut.Name}} {{$shortcut.Description}} {{$shortcut.AppName}}">
        <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="channel" value="{{$.Channel}}"><input type="hidden" name="app_id" value="{{$shortcut.AppID}}"><input type="hidden" name="callback_id" value="{{$shortcut.CallbackID}}">
        <button type="submit"><strong>{{$shortcut.Name}}</strong><span>{{$shortcut.Description}}<small>{{$shortcut.AppName}}</small></span></button>
      </form>{{end}}</div>
    <p class="shortcut-browser-empty" id="shortcut-browser-empty" role="status" hidden>No matching shortcuts.</p>
  </dialog>{{end}}
  <dialog class="keyboard-help" id="keyboard-help" aria-labelledby="keyboard-help-title">
    <div class="keyboard-help-head">
      <h2 id="keyboard-help-title">Keyboard shortcuts</h2>
      <label class="visually-hidden" for="keyboard-help-query">Search shortcuts</label>
      <input id="keyboard-help-query" type="search" autocomplete="off" maxlength="80" placeholder="Search shortcuts">
      <button id="keyboard-help-close" type="button" aria-label="Close keyboard shortcuts">&times;</button>
    </div>
    <div class="keyboard-help-body">{{range .Keyboard}}
      <section data-keyboard-section aria-labelledby="keyboard-section-{{.Title}}">
        <h3 id="keyboard-section-{{.Title}}">{{.Title}}</h3>
        <dl>{{range .Shortcuts}}
          <div data-keyboard-row data-keyboard-search="{{.Search}}">
            <dt>{{.Action}}{{if .Note}}<small>{{.Note}}</small>{{end}}</dt>
            <dd><kbd data-keyboard-apple>{{.Apple}}</kbd><kbd data-keyboard-other>{{.Other}}</kbd></dd>
          </div>{{end}}
        </dl>
      </section>{{end}}
    </div>
    <p class="keyboard-help-empty" id="keyboard-help-empty" role="status" hidden>No matching shortcuts.</p>
  </dialog>
  {{if and .CanPost .CanUpload}}<dialog class="clip-recorder" id="clip-recorder" aria-labelledby="clip-recorder-title" aria-describedby="clip-recorder-status">
    <h2 id="clip-recorder-title">Record a clip</h2>
    <p id="clip-recorder-status" role="status" aria-live="polite">Choose an audio or video clip from the composer.</p>
    <video id="clip-recorder-preview" autoplay muted playsinline hidden></video>
    <div class="clip-recorder-actions"><button type="button" id="clip-recorder-cancel">Cancel</button><button class="clip-stop" type="button" id="clip-recorder-stop" disabled>Stop recording</button></div>
  </dialog>{{end}}
  <div class="workspace">
    <aside class="sidebar" id="workspace-sidebar">
      <div>
        {{if .Workspaces}}<details class="workspace-switch">
          <summary class="workspace-name">{{.WorkspaceName}}</summary>
          <form method="post" action="/app/workspace/switch">
            <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
            <ul class="workspace-list" aria-label="Your workspaces">{{range .Workspaces}}<li>{{if .Current}}<span class="workspace-current">{{.Name}} <small>{{.Role}} · you are here</small></span>{{else}}<button type="submit" name="workspace_id" value="{{.ID}}">{{.Name}} <small>{{.Role}}</small></button>{{end}}</li>{{end}}</ul>
          </form>
        </details>{{else}}<div class="workspace-name">{{.WorkspaceName}}</div>{{end}}
        <div class="workspace-sub"><span class="presence-dot" aria-hidden="true"></span>{{.Username}}</div>
      </div>
      <nav class="side-section" aria-label="Workspace navigation">
        <div class="side-label">Workspace</div>
        <a class="side-link" href="/app/unreads?channel={{.Channel}}" aria-label="Unreads" {{ariaKeyshortcuts "Unreads"}}><span class="side-icon" aria-hidden="true">◍</span><span class="side-text">Unreads</span></a>
        <a class="side-link" href="/app/threads?channel={{.Channel}}" aria-label="Threads" {{ariaKeyshortcuts "Threads"}}><span class="side-icon" aria-hidden="true">⌸</span><span class="side-text">Threads</span></a>
        <a class="side-link" id="activity-link" href="/app/activity?channel={{.Channel}}" aria-label="Activity{{if .ReminderUnread}}, reminder due{{end}}" {{ariaKeyshortcuts "Activity"}}><span class="side-icon" aria-hidden="true">◉</span><span class="side-text">Activity</span>{{if .ReminderUnread}}<span class="badge" aria-hidden="true">•</span>{{end}}</a>
        <a class="side-link" href="/app/notifications?channel={{.Channel}}" aria-label="Notification preferences"><span class="side-icon" aria-hidden="true">◌</span><span class="side-text">Notifications</span></a>
        <a class="side-link" href="/app/later?channel={{.Channel}}" aria-label="Later{{if .ReminderUnread}}, reminder due{{end}}" {{ariaKeyshortcuts "Later"}}><span class="side-icon" aria-hidden="true">▱</span><span class="side-text">Later</span>{{if .ReminderUnread}}<span class="badge" aria-hidden="true">•</span>{{end}}</a>
        {{if .CanSchedule}}<a class="side-link" href="/app/drafts?channel={{.Channel}}" aria-label="Drafts and sent"><span class="side-icon" aria-hidden="true">◷</span><span class="side-text">Drafts &amp; sent</span></a>{{end}}
        <a class="side-link" href="/app/dms" aria-label="Direct messages" {{ariaKeyshortcuts "Direct messages"}}><span class="side-icon" aria-hidden="true">⌁</span><span class="side-text">DMs</span></a>
        <a class="side-link" href="/app/members" aria-label="Members"><span class="side-icon" aria-hidden="true">@</span><span class="side-text">People</span></a>
        <a class="side-link" href="/app/remote-files?channel={{.Channel}}" aria-label="Remote files"><span class="side-icon" aria-hidden="true">⇗</span><span class="side-text">Remote files</span></a>
        <a class="side-link" href="/app/canvases" aria-label="Canvases"><span class="side-icon" aria-hidden="true">▤</span><span class="side-text">Canvases</span></a>
        <a class="side-link" href="/app/lists" aria-label="Lists"><span class="side-icon" aria-hidden="true">☷</span><span class="side-text">Lists</span></a>
        <a class="side-link" href="/app/workflows" aria-label="Workflows"><span class="side-icon" aria-hidden="true">⌁</span><span class="side-text">Workflows</span></a>
        <a class="side-link" href="/app/apps?channel={{.Channel}}" aria-label="Apps"><span class="side-icon" aria-hidden="true">◇</span><span class="side-text">Apps</span></a>
        <a class="side-link" href="/app/developer/apps" aria-label="Developer apps"><span class="side-icon" aria-hidden="true">⌘</span><span class="side-text">Developer apps</span></a>
        {{if .ShowAdmin}}<a class="side-link" href="/app/admin/settings" aria-label="Workspace settings"><span class="side-icon" aria-hidden="true">⚙</span><span class="side-text">Workspace settings</span></a>{{end}}
        {{if .ShowAuthAdmin}}<a class="side-link" href="/app/admin/auth" aria-label="Authorization"><span class="side-icon" aria-hidden="true">A</span><span class="side-text">Authorization</span></a>{{end}}
      </nav>
      {{if .Apps}}<nav class="side-section" aria-label="Apps"><div class="side-label">Apps</div>{{range .Apps}}<a class="side-link" href="/app/apps/{{.ID}}?channel={{$.Channel}}"><span class="side-icon" aria-hidden="true">◇</span><span class="side-text">{{.Name}}</span></a>{{end}}</nav>{{end}}
      <nav class="side-section" aria-label="Channels">
        <div class="side-label">Channels</div>
        {{range .Channels}}
        <a class="side-link" href="/app?channel={{.ID}}"{{if .Current}} aria-current="page"{{end}} aria-label="{{.Name}}{{if .UnreadCount}}, {{.UnreadCount}} unread messages{{end}}{{if .HasDraft}}, has a draft{{end}}">
          <span class="side-icon" aria-hidden="true">#</span><span class="side-text">{{.Name}}</span>
          {{if .HasDraft}}<span class="draft-badge" aria-hidden="true">Draft</span>{{else if .UnreadCount}}<span class="badge" aria-hidden="true">{{.UnreadCount}}</span>{{end}}
        </a>
        {{else}}<p class="side-empty">No channels available.</p>{{end}}
        {{if .CanCreate}}
        <details class="new-channel">
          <summary>＋ Add channel</summary>
          <form method="post" action="/app/conversation/create" hx-post="/app/conversation/create">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="new-channel-name">Channel name<input id="new-channel-name" type="text" name="name" maxlength="80" placeholder="project-name" required></label>
            <label class="privacy"><input type="checkbox" name="is_private" value="true"> Private channel</label>
            <button type="submit">Create</button>
          </form>
        </details>
        {{end}}
      </nav>
      {{if .Directs}}
      <nav class="side-section" aria-label="Direct messages">
        <div class="side-label">Direct messages</div>
        {{range .Directs}}
        <a class="side-link" href="/app?channel={{.ID}}"{{if .Current}} aria-current="page"{{end}} aria-label="{{.Name}}{{if .UnreadCount}}, {{.UnreadCount}} unread messages{{end}}{{if .HasDraft}}, has a draft{{end}}">
          <span class="side-icon" aria-hidden="true">@</span><span class="side-text">{{.Name}}</span>
          {{if .HasDraft}}<span class="draft-badge" aria-hidden="true">Draft</span>{{else if .UnreadCount}}<span class="badge" aria-hidden="true">{{.UnreadCount}}</span>{{end}}
        </a>
        {{end}}
      </nav>
      {{end}}
      {{if .MoreChannelsURL}}<a class="side-more" href="{{.MoreChannelsURL}}">More conversations</a>{{end}}
      <div class="sidebar-bottom">
        <div class="signed-in" data-shauth-user="{{.Username}}"><span class="signed-in-avatar" aria-hidden="true">{{.UserInitial}}</span><span class="signed-in-name">{{.Username}}</span></div>
        <form method="post" action="/app/session/revoke">
          <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
          <button class="side-link" type="submit" data-shauth-sign-out aria-label="Sign out"><span class="side-icon" aria-hidden="true">→</span><span class="side-text">Sign out</span></button>
        </form>
      </div>
    </aside>
    <button class="nav-scrim" id="nav-scrim" type="button" aria-label="Close navigation" tabindex="-1"></button>
    <main class="content" id="content">
      <header class="channel-header">
        <div class="channel-identity">
          <div class="channel-copy">
            <h1 class="channel-title">{{if .ChannelPrefix}}{{.ChannelPrefix}} {{end}}{{.ChannelName}}{{if .ChannelStatusDisplay}}<span class="channel-title-status"{{if .ChannelStatusText}} title="{{.ChannelStatusText}}"{{end}}>{{.ChannelStatusDisplay}}</span>{{end}}</h1>
            <p class="channel-meta">{{if .MemberCount}}<a class="member-count" href="/app?channel={{.Channel}}&amp;details=1" aria-label="{{.MemberCount}} member{{if ne .MemberCount 1}}s{{end}} — open the member list">{{.MemberCount}} member{{if ne .MemberCount 1}}s{{end}}</a> · {{end}}{{.ChannelMeta}}</p>
          </div>
          <span class="membership-pill{{if .IsMember}} joined{{end}}">{{if .IsMember}}Joined{{else}}Not joined{{end}}</span>
        </div>
        <div class="channel-actions">
          <p class="action-feedback" id="action-feedback" role="alert" tabindex="-1" hidden></p>
          {{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
          <details class="channel-overflow"><summary role="button" aria-label="More channel actions">⋮</summary>
            <div class="channel-overflow-menu" aria-label="Channel actions">
              {{if .MarkReadURL}}
              <form class="inline-form" id="mark-read" method="post" action="{{.MarkReadURL}}" hx-post="{{.MarkReadURL}}" data-quiet="true">
                <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="ts" value="{{.MarkReadTimestamp}}">
                <button type="submit">Mark as read</button>
              </form>
              {{end}}
              <form class="inline-form" id="mark-all-read" method="post" action="/app/read/all?channel={{.Channel}}">
                <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
                <button type="submit" {{ariaKeyshortcuts "Mark every conversation read"}}>Mark all as read</button>
              </form>
              <button type="button" id="open-keyboard-help" aria-haspopup="dialog" aria-controls="keyboard-help" {{ariaKeyshortcuts "Keyboard shortcuts"}}>Keyboard shortcuts</button>
            </div>
          </details>
          {{if .ThreadTimestamp}}<a href="/app?channel={{.Channel}}">Back to channel</a>{{end}}
          {{if .CanvasURL}}<a href="{{.CanvasURL}}" aria-label="Open the canvas for this conversation">Canvas</a>{{end}}
          <a href="/app?channel={{.Channel}}&amp;details=1" aria-label="Open conversation details" {{ariaKeyshortcuts "Conversation details"}}>Details</a>
        </div>
      </header>
      <div class="timeline-wrap">
        {{if .OlderURL}}<p class="pager pager-older"><a href="{{.OlderURL}}">Show older messages</a></p>{{end}}
        <div id="huddle" data-fragment="{{.HuddleURL}}" data-live="true">{{template "huddle" .Huddle}}</div>
        <section id="timeline" class="timeline" tabindex="0" aria-label="Messages" data-fragment="{{.TimelineURL}}" data-live="{{if .AtLatest}}true{{else}}false{{end}}">{{template "messages" .Timeline}}</section>
        {{if .LatestURL}}<p class="pager pager-newer"><a href="{{.LatestURL}}">Jump to the latest messages</a></p>{{end}}
      </div>
      {{if .ThreadTimestamp}}
      <aside class="thread" aria-labelledby="thread-heading">
        <div class="thread-heading"><h2 id="thread-heading">Thread</h2>{{if .ThreadFollowURL}}<form method="post" action="{{.ThreadFollowURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="followed" value="{{if .FollowingThread}}false{{else}}true{{end}}"><button type="submit" aria-pressed="{{if .FollowingThread}}true{{else}}false{{end}}">{{if .FollowingThread}}Following{{else}}Follow thread{{end}}</button></form>{{end}}</div>
        {{if .Assistant.Present}}<div class="assistant-state">
          {{if .Assistant.Title}}<p class="assistant-title">{{.Assistant.Title}}</p>{{end}}
          {{if .Assistant.Status}}<p class="assistant-status" role="status">{{.Assistant.Status}}</p>{{end}}
          {{if .Assistant.Prompts}}<div class="assistant-prompts">
            {{if .Assistant.PromptsTitle}}<p class="assistant-prompts-title">{{.Assistant.PromptsTitle}}</p>{{end}}
            {{range .Assistant.Prompts}}<form method="post" action="{{$.ComposeURL}}" hx-post="{{$.ComposeURL}}">
              <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="text" value="{{.Message}}">
              <button class="assistant-prompt" type="submit" title="{{.Message}}">{{.Title}}</button>
            </form>{{end}}
          </div>{{end}}
        </div>{{end}}
        <div id="thread-messages" tabindex="-1" data-fragment="{{.ThreadURL}}" data-live="true">{{template "messages" .Thread}}</div>
      </aside>
      {{end}}
      <div class="composer-wrap">
        <p class="live-status" id="live-status" role="status" aria-live="polite"></p>
        {{if .CanPost}}
        {{if .CanUpload}}<form class="upload-form" id="upload-form" method="post" action="{{.StageUploadURL}}" enctype="multipart/form-data" hidden>
          <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
          <input type="hidden" id="upload-comment" name="text" value="{{.Draft}}">
          <input type="hidden" id="upload-draft-attachments" name="draft_attachments" value="{{.DraftJSON}}">
          <input id="upload-file" type="file" name="file" multiple aria-label="Files to attach">
        </form>{{end}}
        <div id="typing" data-typing="/app/typing?channel={{.Channel}}" data-channel="{{.Channel}}">{{template "typing" .Typing}}</div>
        <form class="composer{{if .Error}} is-error{{end}}" id="composer" method="post" action="{{.ComposeURL}}" hx-post="{{.ComposeURL}}" hx-target="{{if .ThreadTimestamp}}#thread-messages{{else}}#timeline{{end}}" data-newest="{{.LatestURL}}" data-draft-url="{{.DraftURL}}">
          <p class="form-error" id="composer-error" role="alert" tabindex="-1"{{if .Error}} autofocus{{end}}{{if not .Error}} hidden{{end}}>{{.Error}}</p>
          {{if .CanUpload}}<p class="upload-preview" id="upload-preview" role="status">{{if .DraftAttachments}}{{range $index, $attachment := .DraftAttachments}}{{if $index}} · {{end}}{{$attachment.Name}}{{end}}{{end}}</p>
          <button type="button" id="upload-clear" hidden>Remove staged files</button>{{end}}
          <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
          <input type="hidden" name="timezone" data-browser-timezone value="UTC">
          <input type="hidden" id="draft-attachments" name="draft_attachments" value="{{.DraftJSON}}">
          <label class="visually-hidden" for="text">{{if .ThreadTimestamp}}Reply in the thread{{else}}Message {{.ChannelPrefix}}{{.ChannelName}}{{end}}</label>
          <textarea id="text" name="text" maxlength="40000"{{if not .DraftAttachments}} required{{end}}{{if not .Error}} autofocus{{end}} role="combobox" aria-describedby="composer-hint" aria-keyshortcuts="Enter Shift+Enter Control+B Meta+B Control+I Meta+I Control+Shift+X Meta+Shift+X" aria-autocomplete="list" aria-controls="mention-suggestions channel-suggestions emoji-suggestions slash-suggestions" aria-expanded="false" placeholder="{{if .ThreadTimestamp}}Reply in the thread{{else}}Message {{.ChannelPrefix}}{{.ChannelName}}{{end}}">{{.Draft}}</textarea>
          {{if or .ComposerMembers .ComposerGroups}}<div class="mention-suggestions" id="mention-suggestions" role="listbox" aria-label="Mention suggestions" hidden>{{range .ComposerMembers}}
            <button type="button" role="option" data-mention-user="{{.ID}}" data-mention-name="{{.Name}}" data-mention-search="{{.Name}}"><span>@{{.Name}}{{if .IsSelf}} (you){{end}}</span><small>Person</small></button>{{end}}{{range .ComposerGroups}}
            <button type="button" role="option" data-mention-group="{{.ID}}" data-mention-name="{{.Handle}}" data-mention-search="{{.Handle}} {{.Name}} {{.Description}}"><span>@{{.Handle}}</span><small>{{.Name}} · {{.MemberCount}} members</small></button>{{end}}
          </div>{{end}}
          {{if .ComposerChannels}}<div class="channel-suggestions" id="channel-suggestions" role="listbox" aria-label="Channel suggestions" hidden>{{range .ComposerChannels}}
            <button type="button" role="option" data-channel-id="{{.ID}}" data-channel-name="{{.Name}}">#{{.Name}}</button>{{end}}
          </div>{{end}}
          <div class="emoji-suggestions" id="emoji-suggestions" role="listbox" aria-label="Emoji suggestions" hidden></div>
          {{if .SlashCommands}}<div class="slash-suggestions" id="slash-suggestions" role="listbox" aria-label="Shortcuts and slash commands" hidden>{{range .SlashCommands}}
            <button type="button" role="option" data-slash-command="{{.Command}}" data-slash-search="{{.Command}} {{.Description}} {{.UsageHint}} {{.AppName}}"><strong>{{.Command}}</strong><span>{{.Description}}{{if .UsageHint}} <small>{{.UsageHint}}</small>{{end}}{{if .AppName}}<small>{{.AppName}}</small>{{end}}</span></button>{{end}}
          </div>{{end}}
          {{if .ThreadTimestamp}}<input type="hidden" name="thread_ts" value="{{.ThreadTimestamp}}">{{end}}
          <div class="composer-footer">
          <div class="composer-toolbar" role="toolbar" aria-label="Message formatting and insertions">
            {{if or .CanUpload .SlashCommands .GlobalShortcuts}}<details class="composer-menu composer-plus"><summary role="button" aria-label="Attach a file or browse shortcuts" aria-controls="composer-plus-menu">＋</summary>
              <div class="composer-popover" id="composer-plus-menu" aria-label="Attach and shortcuts">
                {{if .CanUpload}}<button type="button" id="composer-attach" aria-controls="upload-file">Upload from computer</button>{{end}}
                {{if or .SlashCommands .GlobalShortcuts}}<button type="button" id="open-shortcut-browser" aria-label="Browse shortcuts" aria-haspopup="dialog" aria-controls="shortcut-browser">Browse shortcuts</button>{{end}}
              </div>
            </details>{{end}}
            <button class="composer-tool" type="button" data-wrap="*" aria-label="Bold" aria-controls="text"><strong>B</strong></button>
            <button class="composer-tool" type="button" data-wrap="_" aria-label="Italic" aria-controls="text"><em>I</em></button>
            <button class="composer-tool" type="button" data-wrap="~" aria-label="Strikethrough" aria-controls="text"><s>S</s></button>
            <button class="composer-tool" type="button" data-wrap="&#96;" aria-label="Inline code" aria-controls="text">&lt;/&gt;</button>
            <button class="composer-tool" type="button" data-insert="&lt;https://example.com|link text&gt;" data-select-offset="1" data-select-length="19" aria-label="Insert link" aria-controls="text">🔗</button>
            <button class="composer-tool" type="button" data-open-emoji-picker data-emoji-target="composer" aria-label="Choose an emoji" aria-haspopup="dialog" aria-controls="emoji-picker-dialog">☺</button>
            {{if .CanUpload}}<button class="composer-tool" type="button" data-record-clip="audio" aria-label="Record audio clip" aria-haspopup="dialog" aria-controls="clip-recorder">🎤</button>
            <button class="composer-tool" type="button" data-record-clip="video" aria-label="Record video clip" aria-haspopup="dialog" aria-controls="clip-recorder">🎥</button>{{end}}
            {{if or .ComposerMembers .ComposerGroups}}<details class="composer-menu"><summary role="button" aria-label="Mention a person or user group" aria-controls="mention-picker">@</summary>
              <div class="composer-popover" id="mention-picker" role="menu" aria-label="People and user groups">{{range .ComposerMembers}}
                <button type="button" data-mention-user="{{.ID}}" data-mention-name="{{.Name}}" data-mention-search="{{.Name}}" role="menuitem"><span>@{{.Name}}{{if .IsSelf}} (you){{end}}</span><small>Person</small></button>{{end}}{{range .ComposerGroups}}
                <button type="button" data-mention-group="{{.ID}}" data-mention-name="{{.Handle}}" data-mention-search="{{.Handle}} {{.Name}} {{.Description}}" role="menuitem"><span>@{{.Handle}}</span><small>{{.Name}} · {{.MemberCount}} members</small></button>{{end}}
              </div>
            </details>{{end}}
          </div>
            <span class="composer-tools" id="composer-hint"><kbd>Enter</kbd> sends · <kbd>Shift</kbd> + <kbd>Enter</kbd> adds a line{{if .CanUpload}} · You can also paste or drop files into the composer.{{end}}</span>
            <div class="send-actions"><button class="send" type="submit">Send</button><details class="schedule-menu"><summary role="button" aria-label="Schedule message">⌄</summary><div class="schedule-popover"><label for="schedule-at">Send date and time<input id="schedule-at" type="datetime-local" name="schedule_at" value="{{.ScheduleAt}}" data-schedule-at aria-describedby="schedule-time-help"></label><input type="hidden" name="post_at"><p id="schedule-time-help">The time uses your current browser time zone and must be within 120 days.</p><button type="submit" formaction="{{.ScheduleURL}}">Schedule message</button><a href="/app/drafts?channel={{.Channel}}&amp;tab=scheduled">View scheduled messages</a></div></details></div>
          </div>
        </form>
        {{else}}
        <section class="conversation-gate" aria-label="Conversation access">
          <div class="conversation-gate-copy">
            {{if .IsMember}}
            <strong>This conversation is read-only for your session.</strong>
            <p>Your current permissions allow reading messages but not posting them.</p>
            {{else}}
            <strong>Join {{.ChannelPrefix}}{{.ChannelName}} to take part.</strong>
            {{end}}
          </div>
          {{if .CanJoin}}
          <form method="post" action="{{.JoinURL}}">
            <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
            <button class="join-button" type="submit">Join channel</button>
          </form>
          {{end}}
        </section>
        {{end}}
      </div>
    </main>
  </div>
</div>
{{if .Details}}
<div class="conversation-details-backdrop">
  <section class="conversation-details" role="dialog" aria-modal="true" aria-labelledby="conversation-details-title">
    <header class="conversation-details-head">
      <div><h2 id="conversation-details-title">{{if .Details.IsChannel}}# {{end}}{{.Details.Name}}</h2><p>{{.Details.Type}}{{if .Details.Archived}} · Archived{{end}}</p></div>
      <a class="conversation-details-close" href="{{.Details.CloseURL}}" aria-label="Close conversation details">×</a>
    </header>
    <div class="conversation-details-body">
      <nav class="conversation-details-tabs" aria-label="Conversation details sections">
        <a href="#conversation-about-heading">About</a>
        <a href="#conversation-members-heading">Members</a>
        {{if or .Details.CanEdit .Details.CanConvert .Details.CanChangeVisibility}}<a href="#conversation-settings-heading">Settings</a>{{end}}
      </nav>
      <section class="conversation-details-section" aria-labelledby="conversation-about-heading">
        <h3 id="conversation-about-heading">About</h3>
        <dl class="conversation-facts">
          <div><dt>Topic</dt><dd>{{if .Details.Topic}}{{.Details.Topic}}{{else}}No topic set{{end}}</dd></div>
          <div><dt>Purpose</dt><dd>{{if .Details.Purpose}}{{.Details.Purpose}}{{else}}No purpose set{{end}}</dd></div>
        </dl>
        {{if .Details.CanEdit}}
        <div class="conversation-settings">
          <form class="conversation-setting" method="post" action="/app/conversation/rename?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="conversation-name">Channel name<input id="conversation-name" name="name" value="{{.Details.Name}}" maxlength="80" required></label>
            <button type="submit">Rename</button>
          </form>
          <form class="conversation-setting" method="post" action="/app/conversation/topic?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="conversation-topic">Topic<textarea id="conversation-topic" name="topic" maxlength="250">{{.Details.Topic}}</textarea></label>
            <button type="submit">Save topic</button>
          </form>
          <form class="conversation-setting" method="post" action="/app/conversation/purpose?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="conversation-purpose">Purpose<textarea id="conversation-purpose" name="purpose" maxlength="250">{{.Details.Purpose}}</textarea></label>
            <button type="submit">Save purpose</button>
          </form>
        </div>
        {{end}}
      </section>
      {{if .Details.CanNotify}}
      <section class="conversation-details-section" id="conversation-notifications" aria-labelledby="conversation-notifications-heading">
        <h3 id="conversation-notifications-heading">Notifications</h3>
        <form class="conversation-setting" method="post" action="/app/conversation/notifications?channel={{.Details.ID}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <label for="conversation-notification-level">Notify me about
            <select id="conversation-notification-level" name="level">
              <option value="inherit"{{if eq .Details.NotificationLevel "inherit"}} selected{{end}}>Use workspace default</option>
              <option value="all"{{if eq .Details.NotificationLevel "all"}} selected{{end}}>All new posts</option>
              <option value="mentions"{{if eq .Details.NotificationLevel "mentions"}} selected{{end}}>Just mentions</option>
              <option value="mute"{{if eq .Details.NotificationLevel "mute"}} selected{{end}}>Mute conversation</option>
            </select>
          </label>
          <label class="privacy"><input type="checkbox" name="follow_every_thread" value="true"{{if .Details.FollowEveryThread}} checked{{end}}> Follow every thread</label>
          <button type="submit">Save notifications</button>
        </form>
      </section>
      {{end}}
      <section class="conversation-details-section" aria-labelledby="conversation-members-heading">
        <h3 id="conversation-members-heading">Members ({{len .Details.Members}})</h3>
        <ul class="conversation-members">{{range .Details.Members}}<li class="conversation-member"><span class="presence {{.Presence}}" aria-hidden="true"></span><span class="conversation-member-avatar" aria-hidden="true">{{.AuthorInitial}}</span><span class="conversation-member-name">{{.Name}} <span class="visually-hidden">({{if eq .Presence "active"}}active{{else if eq .Presence "away"}}away{{else}}automatic; activity unavailable{{end}})</span></span>{{if .StatusDisplay}}<span class="conversation-member-status"{{if .Profile.StatusText}} title="{{.Profile.StatusText}}"{{end}}>{{.StatusDisplay}}</span>{{end}}</li>{{end}}</ul>
        {{if .Details.Truncated}}<p class="conversation-details-note">This workspace has more than 1,000 members. Use the member directory to find people outside this list.</p>{{end}}
        {{if .Details.CanInvite}}
          {{if .Details.Invitees}}
          <form class="conversation-setting" method="post" action="/app/conversation/invite?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="conversation-invitee">Add a person<select id="conversation-invitee" name="user" required><option value="" selected disabled>Choose a workspace member</option>{{range .Details.Invitees}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
            <button type="submit">Add</button>
          </form>
          {{else}}<p class="conversation-details-note">Every available workspace member is already in this channel.</p>{{end}}
        {{end}}
        {{if .Details.CanAddPeople}}
        <details class="conversation-setting">
          <summary>Add people</summary>
          <form method="post" action="/app/conversation/add-people?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <fieldset>
              <legend>Choose people to add</legend>
              {{range .Details.Invitees}}<label class="privacy"><input type="checkbox" name="user_{{.ID}}" value="1"> {{.Name}}</label>{{end}}
            </fieldset>
            <p class="conversation-details-note">Slack creates a new group DM. This conversation and its members stay unchanged.</p>
            <button type="submit">Next</button>
          </form>
        </details>
        {{end}}
      </section>
      {{if or .Details.CanEdit .Details.CanConvert .Details.CanChangeVisibility}}
      <section class="conversation-details-section" aria-labelledby="conversation-settings-heading">
        <h3 id="conversation-settings-heading">Settings</h3>
        {{if .Details.CanConvert}}
        <details class="conversation-setting">
          <summary>Change to a private channel</summary>
          <form method="post" action="/app/conversation/convert-to-private?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <label for="converted-channel-name">Private channel name<input id="converted-channel-name" name="name" maxlength="80" required pattern="[A-Za-z0-9_-]+"></label>
            <p class="conversation-details-note">Messages and files from this group DM will stay in the new private channel and will be visible to members added later. Everyone in this group DM will be notified.</p>
            <button type="submit">Change to Private</button>
          </form>
        </details>
        {{end}}
        {{if .Details.CanChangeVisibility}}
        <details class="conversation-setting">
          <summary>{{if .Details.IsPrivate}}Make this channel public{{else}}Make this channel private{{end}}</summary>
          <form method="post" action="/app/conversation/visibility?channel={{.Details.ID}}">
            <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
            <input type="hidden" name="private" value="{{if .Details.IsPrivate}}false{{else}}true{{end}}">
            <p class="conversation-details-note">{{if .Details.IsPrivate}}Everyone in the workspace will be able to read everything already said in this channel. That cannot be undone by making it private again.{{else}}Only members will be able to read this channel. Everything already said stays, and stays visible to the people already in it.{{end}}</p>
            <button type="submit">{{if .Details.IsPrivate}}Make public{{else}}Make private{{end}}</button>
          </form>
        </details>
        {{end}}
      </section>
      {{end}}
      {{if .Details.IsChannel}}
      {{if .Details.CanSetRetention}}
      <section class="conversation-details-section" id="conversation-retention" aria-labelledby="conversation-retention-heading">
        <h3 id="conversation-retention-heading">How long messages are kept</h3>
        <p>{{.Details.RetentionSummary}}</p>
        <form class="connect-invite" method="post" action="/app/conversation/retention?channel={{.Details.ID}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <label>Keep for<input name="duration_days" type="number" min="1" max="36499" value="{{.Details.RetentionDays}}"><span class="read-only">days</span></label>
          <button type="submit">Use this limit here</button>
        </form>
        {{if .Details.RetentionCustom}}<form method="post" action="/app/conversation/retention/remove?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Follow the workspace default instead</button></form>{{end}}
        <p class="read-only">Deletion is permanent. A shorter limit here deletes sooner than the workspace default; it cannot keep messages for longer than a channel-specific limit would.</p>
      </section>
      {{end}}
      <section class="conversation-details-section" id="conversation-connect" aria-labelledby="conversation-connect-heading">
        <h3 id="conversation-connect-heading">Shared with other organizations</h3>
        {{if .Details.Connected}}<p>In this channel: {{range $index, $org := .Details.Connected}}{{if $index}}, {{end}}{{$org.Name}}{{end}}</p>{{else}}<p class="read-only">Only this workspace is in this channel.</p>{{end}}
        {{if .Details.Outstanding}}<ul class="connect-invites" aria-label="Outstanding invitations">{{range .Details.Outstanding}}<li>
          <span><strong>{{.Target}}</strong> <span class="status">{{.Status}}</span>{{if .Expired}}<br><span class="status">expired on {{.Expires}} and can no longer be approved</span>{{else if .Expires}}<br><span class="status">valid until {{.Expires}}</span>{{end}}</span>
          <span class="connect-actions">
          {{if .CanApprove}}<form method="post" action="{{.ApproveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_id" value="{{.ID}}"><button type="submit">Approve</button></form>{{end}}
          {{if .CanRevoke}}<form method="post" action="{{.DenyURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="invite_id" value="{{.ID}}"><button type="submit">Withdraw</button></form>{{end}}
          </span>
        </li>{{end}}</ul>{{end}}
        {{if .Details.CanConnect}}
        <form class="connect-invite" method="post" action="{{.Details.ConnectURL}}">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}">
          <label>Invite an organization<select name="target">{{range .Details.ConnectHosts}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
          <button type="submit">Send invitation</button>
        </form>
        <p class="read-only">An invitation is recorded here and takes effect only when the other organization accepts it. Everyone there will be able to read this channel's history from the moment they join.</p>
        {{end}}
      </section>
      {{end}}
      {{if or .Details.CanArchive .Details.CanLeave .Details.CanClose}}
      <section class="conversation-danger" aria-label="Conversation actions">
        {{if .Details.CanArchive}}<form method="post" action="/app/conversation/archive?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="archived" value="{{if .Details.Archived}}false{{else}}true{{end}}"><button type="submit">{{.Details.ArchiveVerb}} channel</button></form>{{end}}
        {{if .Details.CanLeave}}<form method="post" action="/app/conversation/leave?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Leave channel</button></form>{{end}}
        {{if .Details.CanClose}}<form method="post" action="/app/conversation/leave?channel={{.Details.ID}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Close conversation</button></form>{{end}}
      </section>
      {{end}}
    </div>
  </section>
</div>
{{end}}
{{if .Modal}}
<div class="modal-backdrop">
  <section class="app-modal" role="dialog" aria-modal="true" aria-labelledby="app-modal-title">
    <form id="modal-close-form" method="post" action="/app/view/close?channel={{.Channel}}">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="view_id" value="{{.Modal.ID}}"><input type="hidden" name="clear" value="{{.Modal.ClearOnClose}}">
    </form>
    <header class="modal-head"><div><h2 id="app-modal-title">{{.Modal.Title}}</h2><span class="modal-app">App modal</span></div><button class="modal-close-x" type="submit" form="modal-close-form" formnovalidate aria-label="Close {{.Modal.Title}}">×</button></header>
    <form class="modal-form" method="post" action="/app/view/submit?channel={{.Channel}}">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="view_id" value="{{.Modal.ID}}">
      <div class="modal-body">
      {{if .Modal.Error}}<p class="form-error" role="alert" tabindex="-1" autofocus>{{.Modal.Error}}</p>{{end}}
      {{range $block := .Modal.Blocks}}
        {{if $block.Input}}{{$input := $block.Input}}
        <div class="modal-block modal-input">
          {{if or (eq $input.Control "radio") (eq $input.Control "checkbox")}}<fieldset class="modal-options"{{if $block.Error}} aria-describedby="modal-error-{{$input.Index}}"{{end}}><legend class="modal-legend">{{$input.Label}}{{if $input.Optional}} <span class="modal-hint">(optional)</span>{{end}}</legend>
            {{range $option := $input.Options}}<label><input type="{{if eq $input.Control "radio"}}radio{{else}}checkbox{{end}}" name="input_{{$input.Index}}" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}{{if and (eq $input.Control "radio") (not $input.Optional)}} required{{end}}> <span>{{$option.Text}}</span></label>{{end}}
          </fieldset>
          {{else}}<label for="modal-input-{{$input.Index}}">{{$input.Label}}{{if $input.Optional}} <span class="modal-hint">(optional)</span>{{end}}</label>
            {{if eq $input.Control "textarea"}}<textarea id="modal-input-{{$input.Index}}" name="input_{{$input.Index}}" placeholder="{{$input.Placeholder}}"{{if not $input.Optional}} required{{end}}{{if $block.Error}} aria-invalid="true" aria-describedby="modal-error-{{$input.Index}}"{{end}}>{{$input.Value}}</textarea>
            {{else if eq $input.Control "external"}}<div class="external-select" data-app-options data-app-id="{{$.Modal.AppID}}" data-view-id="{{$.Modal.ID}}" data-block-id="{{$input.BlockID}}" data-action-id="{{$input.ActionID}}" data-channel="{{$.Channel}}" data-min-query="{{$input.MinQueryLength}}"><input id="modal-input-{{$input.Index}}" type="search" data-options-query placeholder="{{$input.Placeholder}}" minlength="{{$input.MinQueryLength}}"><button class="block-action" type="button" data-options-load>Search</button><select name="input_{{$input.Index}}" data-options-results{{if $input.Multiple}} multiple{{end}}{{if not $input.Optional}} required{{end}}{{if not $input.Options}} disabled{{end}}{{if $block.Error}} aria-invalid="true" aria-describedby="modal-error-{{$input.Index}}"{{end}}>{{range $option := $input.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select><p class="external-select-status" data-options-status role="status"></p><noscript>Dynamic options require JavaScript in this client.</noscript></div>
            {{else if eq $input.Control "select"}}<select id="modal-input-{{$input.Index}}" name="input_{{$input.Index}}"{{if $input.Multiple}} multiple{{end}}{{if not $input.Optional}} required{{end}}{{if $block.Error}} aria-invalid="true" aria-describedby="modal-error-{{$input.Index}}"{{end}}><option value=""{{if not $input.Optional}} disabled{{end}}>{{$input.Placeholder}}</option>{{range $option := $input.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select>
            {{else}}<input id="modal-input-{{$input.Index}}" type="{{if eq $input.Control "date"}}date{{else if eq $input.Control "time"}}time{{else if eq $input.Control "datetime"}}datetime-local{{else if eq $input.Control "email"}}email{{else if eq $input.Control "url"}}url{{else if eq $input.Control "number"}}number{{else}}text{{end}}" name="input_{{$input.Index}}" value="{{$input.Value}}" placeholder="{{$input.Placeholder}}"{{if not $input.Optional}} required{{end}}{{if $block.Error}} aria-invalid="true" aria-describedby="modal-error-{{$input.Index}}"{{end}}>{{end}}
          {{end}}
          {{if $input.Hint}}<p class="modal-hint">{{$input.Hint}}</p>{{end}}{{if $block.Error}}<p class="modal-error" id="modal-error-{{$input.Index}}" role="alert">{{$block.Error}}</p>{{end}}
        </div>
        {{else if eq $block.Kind "divider"}}<hr class="modal-block divider">
        {{else}}<div class="modal-block message-block {{$block.Kind}}">{{if $block.HTML}}<div class="formatted-text">{{$block.HTML}}</div>{{else if $block.Text}}<div>{{$block.Text}}</div>{{end}}{{if $block.Fields}}<ul class="message-block-fields">{{range $index, $field := $block.Fields}}<li>{{with index $block.FieldHTML $index}}{{.}}{{else}}{{$field}}{{end}}</li>{{end}}</ul>{{end}}{{if $block.Table}}<div class="block-table-wrap"><table class="block-table">{{if $block.Caption}}<caption>{{$block.Caption}}</caption>{{end}}<tbody>{{range $rowIndex, $row := $block.Table}}<tr>{{range $cell := $row}}{{if and $block.HeaderRow (eq $rowIndex 0)}}<th scope="col">{{$cell}}</th>{{else}}<td>{{$cell}}</td>{{end}}{{end}}</tr>{{end}}</tbody></table></div>{{end}}{{if $block.ImageURL}}<img class="message-media" src="{{$block.ImageURL}}" alt="{{$block.ImageAlt}}" loading="lazy">{{end}}
          {{if $block.Actions}}<div class="modal-actions" aria-label="App actions">{{range $action := $block.Actions}}
            {{if eq $action.Control "button"}}<button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>{{$action.Text}}</button>
            {{else if eq $action.Control "date"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="date" name="action_{{$action.Index}}" value="{{$action.Value}}"></label><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>
            {{else if eq $action.Control "time"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="time" name="action_{{$action.Index}}" value="{{$action.Value}}"></label><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>
            {{else if eq $action.Control "datetime"}}<label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="datetime-local" name="action_{{$action.Index}}" value="{{$action.Value}}"></label><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>
            {{else if eq $action.Control "radio"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="radio" name="action_{{$action.Index}}" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>
            {{else if eq $action.Control "checkbox"}}<fieldset class="block-action-options"><legend class="sr-only">{{$action.Text}}</legend>{{range $option := $action.Options}}<label><input type="checkbox" name="action_{{$action.Index}}" value="{{$option.Value}}"{{if $option.Selected}} checked{{end}}> {{$option.Text}}</label>{{end}}</fieldset><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>
            {{else if or (eq $action.Control "text") (eq $action.Control "textarea") (eq $action.Control "email") (eq $action.Control "url") (eq $action.Control "number")}}{{if eq $action.Control "textarea"}}<textarea class="block-action" name="action_{{$action.Index}}" placeholder="{{$action.Text}}">{{$action.Value}}</textarea>{{else}}<input class="block-action" type="{{$action.Control}}" name="action_{{$action.Index}}" value="{{$action.Value}}" placeholder="{{$action.Text}}">{{end}}<button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Send</button>
            {{else if eq $action.Control "external"}}<div class="external-select" data-app-options data-app-id="{{$.Modal.AppID}}" data-view-id="{{$.Modal.ID}}" data-block-id="{{$action.BlockID}}" data-action-id="{{$action.ActionID}}" data-channel="{{$.Channel}}" data-min-query="{{$action.MinQueryLength}}"><label><span class="sr-only">{{$action.Text}}</span><input class="block-action" type="search" data-options-query placeholder="{{$action.Text}}" minlength="{{$action.MinQueryLength}}"></label><button class="block-action" type="button" data-options-load>Search</button><label><span class="sr-only">Results</span><select class="block-action block-action-select" name="action_{{$action.Index}}" data-options-results{{if $action.Multiple}} multiple{{end}}{{if not $action.Options}} disabled{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" data-options-choose formnovalidate{{if not $action.Options}} disabled{{end}}>Choose</button><p class="external-select-status" data-options-status role="status"></p><noscript>Dynamic options require JavaScript in this client.</noscript></div>
            {{else}}<label><span class="sr-only">{{$action.Text}}</span><select class="block-action block-action-select" name="action_{{$action.Index}}"{{if $action.Multiple}} multiple{{end}}>{{range $option := $action.Options}}<option value="{{$option.Value}}"{{if $option.Selected}} selected{{end}}>{{$option.Text}}</option>{{end}}</select></label><button class="block-action" type="submit" name="modal_action" value="{{$action.Index}}" formaction="/app/view/action?channel={{$.Channel}}" formnovalidate>Choose</button>{{end}}
          {{end}}</div>{{end}}
        </div>{{end}}
      {{end}}
      </div>
      <footer class="modal-foot"><button class="modal-button" type="submit" form="modal-close-form" formnovalidate>{{.Modal.Close}}</button>{{if .Modal.Submit}}<button class="modal-button primary" type="submit"{{if .Modal.SubmitDisabled}} disabled aria-disabled="true"{{end}}>{{.Modal.Submit}}</button>{{end}}</footer>
    </form>
  </section>
</div>
{{end}}
{{end}}
` + messagesPartial + huddlePartial + typingPartial

var pageTemplate = mustPage(pageMarkup)

// huddlePartial renders the conversation's huddle bar. HUDDLE-01 forbids
// fabricating a connected state, so the bar reports what the media session
// really does: the controls appear only for somebody who is in the huddle, and
// their pressed state follows the tracks rather than the button.
//
// It is a fragment for the same reason the timeline is: presence changes when
// other people join and leave, and the existing data-fragment/data-live
// refresh already reacts to the durable event stream, so this needs no new
// transport.
const huddlePartial = `{{define "huddle"}}{{if .Visible}}<div class="huddle-bar{{if .Active}} active{{end}}">
{{if .Active}}
<div class="huddle-state" role="status">
  <strong>Huddle in {{.ChannelName}}</strong>
  <span class="huddle-people">{{if .Participants}}{{range $index, $name := .Participants}}{{if $index}}, {{end}}{{$name}}{{end}}{{else}}nobody yet{{end}}</span>
  <span class="huddle-media" data-huddle-status>{{if .Joined}}Connecting your microphone…{{else}}Join to connect your microphone and camera.{{end}}</span>
</div>
<div class="huddle-actions">
  {{if .Joined}}<form method="post" action="{{.LeaveURL}}" hx-post="{{.LeaveURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit">Leave huddle</button></form>
  {{else}}<form method="post" action="{{.JoinURL}}" hx-post="{{.JoinURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit">Join huddle</button></form>{{end}}
  {{if .CanEnd}}<form method="post" action="{{.EndURL}}" hx-post="{{.EndURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="huddle-end" type="submit">End for everyone</button></form>{{end}}
  {{if and .Joined .Invitable}}<details class="huddle-invite"><summary role="button" aria-label="Invite someone to the huddle">Invite</summary>
    <form class="huddle-invite-form" method="post" action="{{.InviteURL}}" hx-post="{{.InviteURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <label for="huddle-invitee">Invite to the huddle<select id="huddle-invitee" name="invitee" required>{{range .Invitable}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></label>
      <button type="submit">Invite</button>
    </form>
  </details>{{end}}
</div>
{{if .Joined}}<div class="huddle-media-session" data-huddle-call="{{.CallID}}" data-huddle-self="{{.SelfID}}" data-huddle-peers="{{range $index, $peer := .PeerIDs}}{{if $index}},{{end}}{{$peer}}{{end}}" data-huddle-signal="{{.SignalURL}}" data-huddle-csrf="{{.CSRFToken}}">
  <div class="huddle-controls">
    <button type="button" data-huddle-control="microphone" aria-pressed="false">Mute microphone</button>
    <button type="button" data-huddle-control="camera" aria-pressed="false">Turn on camera</button>
    <button type="button" data-huddle-control="screen" aria-pressed="false">Share screen</button>
  </div>
  <ul class="huddle-tiles" data-huddle-tiles aria-label="People in this huddle"></ul>
</div>{{end}}
{{else}}
<div class="huddle-actions">
  <form method="post" action="{{.StartURL}}" hx-post="{{.StartURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit">Start a huddle</button></form>
  <span class="huddle-media">Starting a huddle opens a call: your browser connects to each person who joins.</span>
</div>
{{end}}
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
</div>{{end}}{{end}}`

// typingPartial renders who is composing, above the composer where Slack puts
// it. The region is a polite live region so a screen reader mentions it between
// sentences rather than interrupting; an assertive one would talk over the
// message a member is reading in order to say that someone else is writing.
//
// When nobody is typing the region is empty rather than absent, so the live
// region the reader's assistive technology is already watching stays the same
// element instead of being replaced each time.
const typingPartial = `{{define "typing"}}<p class="typing" role="status" aria-live="polite">{{if .Text}}<span class="typing-dots" aria-hidden="true"><i></i><i></i><i></i></span>{{.Text}}{{end}}</p>{{end}}`

const membersMarkup = `{{define "title"}}People · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}
.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}
.bar .theme-toggle{margin-left:auto}
.layout{max-width:1100px;margin:0 auto;padding:28px 22px}
.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}
.heading h1{margin:0 0 4px;font-size:26px}
.muted{color:var(--muted)}
.grid{display:grid;grid-template-columns:minmax(280px,380px) minmax(0,1fr);gap:22px;align-items:start}
.card{background:var(--panel);border:1px solid var(--line);border-radius:10px;padding:20px}
.card h2{margin-top:0}
.profile-summary{display:flex;align-items:center;gap:12px;margin-bottom:18px}
.profile-avatar,.person-avatar{flex:0 0 auto;display:grid;place-items:center;overflow:hidden;background:linear-gradient(135deg,#2f7f9c,#0a6b4f);color:#fff;font-weight:800;text-transform:uppercase}
.profile-avatar{width:56px;height:56px;border-radius:10px;font-size:22px}
.person-avatar{width:42px;height:42px;border-radius:8px;font-size:16px}
.profile-avatar img,.person-avatar img{width:100%;height:100%;object-fit:cover}
.profile-summary p{margin:3px 0}
.field{display:grid;gap:5px;margin:12px 0}
.field input{width:100%;border:1px solid var(--field-line);border-radius:5px;background:var(--bg);color:var(--text);padding:9px}
.field small{color:var(--muted)}
.save{background:var(--ok);color:var(--on-strong);border:0;border-radius:5px;padding:9px 14px;font-weight:700}
.members{display:grid;grid-template-columns:repeat(auto-fill,minmax(200px,1fr));gap:12px}
.person{display:grid;grid-template-columns:42px minmax(0,1fr);gap:10px;background:var(--bg);border:1px solid var(--line);border-radius:8px;padding:14px}
.person-copy{min-width:0}
.person h3{font-size:16px;margin:0}
.person p{margin:5px 0;color:var(--muted)}
.person form{display:inline-block;margin:6px 8px 0 0}.vip-form button[aria-pressed=true]{border-color:var(--action);color:var(--action);font-weight:800}
.person button{border:1px solid var(--line);border-radius:5px;background:var(--panel);color:var(--text);padding:5px 10px}
.presence{display:inline-block;width:9px;height:9px;border:2px solid var(--muted);border-radius:50%;margin-right:5px;vertical-align:middle}.presence.active{border-color:var(--ok);background:var(--ok)}.presence.auto{border-style:dashed}.status-suggestions{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0}.status-suggestions button,.secondary{border:1px solid var(--line);border-radius:6px;background:var(--panel);color:var(--text);padding:7px 9px}.profile-actions{display:flex;flex-wrap:wrap;gap:8px;align-items:center}.availability-form{margin:0 0 18px;padding:12px;border:1px solid var(--line);border-radius:8px}.availability-form label{display:flex;align-items:end;gap:8px}.availability-form select{min-width:150px}.scheduled-statuses{margin-top:22px;padding-top:18px;border-top:1px solid var(--line)}.scheduled-status{margin:12px 0;padding:12px;border:1px solid var(--line);border-radius:8px}.scheduled-status h4{margin:0}.scheduled-actions{display:flex;gap:8px;align-items:center}.danger{border:1px solid var(--danger);color:var(--danger);border-radius:5px;background:transparent;padding:8px 12px;font-weight:700}
@media(max-width:720px){.grid{grid-template-columns:minmax(0,1fr)}.layout{padding:20px 14px}}
</style>{{end}}
{{define "content"}}
<header class="bar"><a href="/app">← Back to chat</a><span>People</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout">
  <div class="heading"><h1>People</h1><p class="muted">Find teammates, start a direct message, and keep your profile current.</p></div>
  <div class="grid">
    <section class="card" aria-labelledby="profile-heading">
      <h2 id="profile-heading">Your profile</h2>
      <div class="profile-summary">
        <span class="profile-avatar">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.UserInitial}}{{end}}</span>
        <div><strong>{{if .Profile.DisplayName}}{{.Profile.DisplayName}}{{else}}Add a display name{{end}}</strong><p class="muted"><span class="presence {{.Presence}}" aria-hidden="true"></span>{{if eq .Presence "active"}}Active{{else if eq .Presence "away"}}Away{{else}}Automatic{{end}}</p>{{if .Profile.StatusText}}<p class="muted">{{if .StatusDisplay}}{{.StatusDisplay}}{{else}}💬{{end}} {{.Profile.StatusText}}{{if .StatusExpires}} · clears <time data-status-expires="{{.StatusExpires}}"></time>{{end}}</p>{{else}}<p class="muted">No status set</p>{{end}}</div>
      </div>
      {{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{end}}
      {{if .CanEditProfile}}<form class="availability-form" method="post" action="/app/presence">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <label for="presence">Availability<select id="presence" name="presence"><option value="auto"{{if eq .Presence "active"}} selected{{end}}>Active (automatic)</option><option value="away"{{if eq .Presence "away"}} selected{{end}}>Away</option></select><button class="save" type="submit">Update availability</button></label>
      </form>{{end}}
      {{if .CanEditProfile}}<form method="post" action="/app/profile">
        <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
        <label class="field" for="display_name">Display name<input id="display_name" name="display_name" maxlength="80" value="{{.Profile.DisplayName}}"><small>The name teammates see in messages.</small></label>
        <div class="status-suggestions" aria-label="Suggested statuses"><button type="button" data-status-text="In a meeting" data-status-emoji=":calendar:">📅 In a meeting</button><button type="button" data-status-text="Commuting" data-status-emoji=":car:">🚗 Commuting</button><button type="button" data-status-text="Out sick" data-status-emoji=":face_with_thermometer:">🤒 Out sick</button><button type="button" data-status-text="Vacationing" data-status-emoji=":palm_tree:">🌴 Vacationing</button><button type="button" data-status-text="Working remotely" data-status-emoji=":house_with_garden:">🏠 Working remotely</button></div>
        <label class="field" for="status_text">Status<input id="status_text" name="status_text" maxlength="100" value="{{.Profile.StatusText}}" placeholder="What are you working on?"></label>
        <label class="field" for="status_emoji">Status emoji<input id="status_emoji" name="status_emoji" maxlength="64" value="{{.Profile.StatusEmoji}}" placeholder=":wave:"></label>
        <label class="field" for="status_expiration_local">Remove status after<input id="status_expiration_local" type="datetime-local" data-status-expiration-local><small>Leave blank to keep the status until you clear it.</small></label>
        <input type="hidden" name="status_expiration" value="{{.StatusExpires}}" data-status-expiration>
        <label class="field" for="avatar_url">Profile photo URL<input id="avatar_url" type="url" maxlength="2048" name="avatar_url" value="{{.AvatarURL}}" placeholder="https://example.com/avatar.jpg"><small>Leave blank to use your initial.</small></label>
        <div class="profile-actions"><button class="save" type="submit">Save changes</button>{{if .Profile.StatusText}}<button class="secondary" type="submit" name="clear_status" value="true">Clear status</button>{{end}}</div>
      </form>
      <section class="scheduled-statuses" aria-labelledby="scheduled-statuses-heading">
        <h3 id="scheduled-statuses-heading">Scheduled</h3>
        <p class="muted">Schedule up to five statuses. You can edit or cancel one until it starts.</p>
        <form class="scheduled-status" method="post" action="/app/status/schedule">
          <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
          <h4>Schedule a status</h4>
          <label class="field" for="scheduled_status_text">Status<input id="scheduled_status_text" name="status_text" maxlength="100" value="{{.DraftScheduled.StatusText}}"></label>
          <label class="field" for="scheduled_status_emoji">Emoji<input id="scheduled_status_emoji" name="status_emoji" maxlength="64" value="{{.DraftScheduled.StatusEmoji}}" placeholder=":calendar:"></label>
          <label class="field" for="scheduled_starts_local">Start<input id="scheduled_starts_local" type="datetime-local" data-unix-local required></label><input type="hidden" name="starts_at" value="{{.DraftScheduled.StartsAt}}" data-unix-value>
          <label class="field" for="scheduled_ends_local">End<input id="scheduled_ends_local" type="datetime-local" data-unix-local required></label><input type="hidden" name="ends_at" value="{{.DraftScheduled.EndsAt}}" data-unix-value>
          <button class="save" type="submit">Save scheduled status</button>
        </form>
        {{range .Scheduled}}<form class="scheduled-status" method="post" action="/app/status/scheduled/update">
          <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="id" value="{{.ID}}">
          <label class="field" for="scheduled-text-{{.ID}}">Status<input id="scheduled-text-{{.ID}}" name="status_text" maxlength="100" value="{{.StatusText}}"></label>
          <label class="field" for="scheduled-emoji-{{.ID}}">Emoji<input id="scheduled-emoji-{{.ID}}" name="status_emoji" maxlength="64" value="{{.StatusEmoji}}"></label>
          <label class="field" for="scheduled-start-{{.ID}}">Start<input id="scheduled-start-{{.ID}}" type="datetime-local" data-unix-local required></label><input type="hidden" name="starts_at" value="{{.StartsAt}}" data-unix-value>
          <label class="field" for="scheduled-end-{{.ID}}">End<input id="scheduled-end-{{.ID}}" type="datetime-local" data-unix-local required></label><input type="hidden" name="ends_at" value="{{.EndsAt}}" data-unix-value>
          <div class="scheduled-actions"><button class="save" type="submit">Save</button><button class="danger" type="submit" formaction="/app/status/scheduled/delete" formnovalidate>Cancel status</button></div>
        </form>{{else}}<p class="muted">No scheduled statuses.</p>{{end}}
      </section>
      {{else}}<p class="muted">Your current permissions allow viewing profiles but not changing yours.</p>{{end}}
    </section>
    <section class="card" aria-labelledby="people-heading">
      <h2 id="people-heading">Workspace members</h2>
      <div class="members">
        {{range .Members}}
        <article class="person">
          <span class="person-avatar">{{if .AvatarURL}}<img src="{{.AvatarURL}}" alt="">{{else}}{{.AuthorInitial}}{{end}}</span>
          <div class="person-copy"><h3><span class="presence {{.Presence}}" aria-hidden="true"></span>{{.Name}} <span class="visually-hidden">({{if eq .Presence "active"}}active{{else if eq .Presence "away"}}away{{else}}automatic; activity unavailable{{end}})</span></h3>{{if and .RealName (ne .RealName .Name)}}<p>{{.RealName}}</p>{{end}}{{if .Profile.StatusText}}<p>{{if .StatusDisplay}}{{.StatusDisplay}}{{else}}💬{{end}} {{.Profile.StatusText}}</p>{{end}}{{if and $.CanMessage (not .IsSelf)}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="users" value="{{.ID}}"><button type="submit" aria-label="Message {{.Name}}">Message</button></form>{{end}}{{if not .IsSelf}}<form class="vip-form" method="post" action="/app/notifications/vips"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="target" value="{{.ID}}"><input type="hidden" name="add" value="{{if .IsVIP}}false{{else}}true{{end}}"><button type="submit" aria-pressed="{{if .IsVIP}}true{{else}}false{{end}}" aria-label="{{if .IsVIP}}Remove {{.Name}} as a VIP{{else}}Mark {{.Name}} as a VIP{{end}}">{{if .IsVIP}}★ VIP{{else}}☆ Mark VIP{{end}}</button></form>{{end}}</div>
        </article>
        {{else}}<p class="muted">No members available.</p>{{end}}
      </div>
      {{if .MoreMembersURL}}<p class="pager"><a href="{{.MoreMembersURL}}">Show more members</a></p>{{end}}
    </section>
  </div>
</main>
{{end}}
{{define "scripts"}}<script>(function(){
var hidden=document.querySelector('[data-status-expiration]');
var local=document.querySelector('[data-status-expiration-local]');
function localValue(date){var offset=date.getTimezoneOffset()*60000;return new Date(date.getTime()-offset).toISOString().slice(0,16)}
if(hidden&&local){var current=Number(hidden.value||0);if(current>0)local.value=localValue(new Date(current*1000));local.addEventListener('change',function(){if(!local.value){hidden.value='0';return}var selected=new Date(local.value);hidden.value=isNaN(selected.getTime())?'':String(Math.floor(selected.getTime()/1000))})}
Array.prototype.forEach.call(document.querySelectorAll('form.scheduled-status'),function(form){var locals=form.querySelectorAll('[data-unix-local]');var values=form.querySelectorAll('[data-unix-value]');Array.prototype.forEach.call(locals,function(local,index){var value=values[index];if(!value)return;var current=Number(value.value||0);if(current>0)local.value=localValue(new Date(current*1000));local.addEventListener('change',function(){if(!local.value){value.value='';return}var selected=new Date(local.value);value.value=isNaN(selected.getTime())?'':String(Math.floor(selected.getTime()/1000))})})});
Array.prototype.forEach.call(document.querySelectorAll('[data-status-text]'),function(button){button.addEventListener('click',function(){var text=document.getElementById('status_text');var emoji=document.getElementById('status_emoji');if(text)text.value=button.getAttribute('data-status-text')||'';if(emoji)emoji.value=button.getAttribute('data-status-emoji')||'';if(text)text.focus()})});
Array.prototype.forEach.call(document.querySelectorAll('[data-status-expires]'),function(node){var value=Number(node.getAttribute('data-status-expires')||0);if(value>0){var date=new Date(value*1000);node.dateTime=date.toISOString();node.textContent=new Intl.DateTimeFormat(undefined,{dateStyle:'medium',timeStyle:'short'}).format(date)}});
})();</script>{{end}}`

var membersTemplate = mustPage(membersMarkup)

const directMessagesMarkup = `{{define "title"}}Direct messages · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar .theme-toggle{margin-left:auto}
.layout{width:min(920px,calc(100% - 32px));margin:28px auto 52px}.heading{display:flex;justify-content:space-between;gap:18px;align-items:end;border-bottom:1px solid var(--line);padding-bottom:18px}.heading h1{margin:0 0 4px;font-size:28px}.muted{color:var(--muted)}
.search{display:flex;gap:8px;margin:20px 0}.search input{flex:1;min-width:0;padding:10px 12px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text)}button{border:1px solid var(--field-line);border-radius:7px;background:var(--panel-strong);color:var(--text);padding:9px 13px;font-weight:800}
.panel{margin-top:22px;padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.panel h2{margin:0 0 6px}.recent,.people{display:grid;gap:8px;margin:14px 0 0;padding:0;list-style:none}.recent-row{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;align-items:center;padding:11px;border:1px solid var(--line);border-radius:8px}.recent-row>a{color:var(--text);font-weight:800;text-decoration:none}.rename{display:flex;gap:6px}.rename input,.group-name{min-width:0;padding:7px 9px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}
.person-choice{display:grid;grid-template-columns:auto minmax(0,1fr);gap:10px;align-items:center;padding:10px;border:1px solid var(--line);border-radius:8px}.person-choice strong{display:block}.compose-actions{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;margin-top:14px}.primary{border:0;background:var(--action);color:var(--on-strong)}.error{color:var(--danger);font-weight:700}
@media(max-width:620px){.layout{width:min(100% - 20px,920px);margin-top:18px}.heading{display:block}.recent-row,.compose-actions{grid-template-columns:minmax(0,1fr)}.rename{display:grid}.search{align-items:stretch}}
</style>{{end}}
{{define "content"}}
<header class="bar"><a href="/app">← Back to chat</a><span>Direct messages</span><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout">
  <div class="heading"><div><h1>Direct messages</h1><p class="muted">Find a conversation or start a DM with up to nine people total.</p></div></div>
  <form class="search" method="get" action="/app/dms"><label class="visually-hidden" for="dm-search">Search direct messages and people</label><input id="dm-search" type="search" name="q" value="{{.Query}}" placeholder="Search direct messages and people"><button type="submit">Search</button></form>
  {{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
  <section class="panel" aria-labelledby="recent-dms"><h2 id="recent-dms">Recent</h2><p class="muted">Closing a DM removes it from this list without deleting its history.</p>
    <ul class="recent">{{range .Recent}}<li class="recent-row">{{if $.Query}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="users" value="{{.OpenUsers}}"><button type="submit">Open {{.Name}}{{if .UnreadCount}} ({{.UnreadCount}} unread){{end}}</button></form>{{else}}<a href="/app?channel={{.ID}}">@ {{.Name}}{{if .UnreadCount}} <span aria-label="{{.UnreadCount}} unread">({{.UnreadCount}})</span>{{end}}</a>{{end}}{{if .IsGroupDirect}}<form class="rename" method="post" action="/app/conversation/rename?channel={{.ID}}&return=dms"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label class="visually-hidden" for="name-{{.ID}}">Group DM name</label><input id="name-{{.ID}}" name="name" maxlength="80" value="{{.Name}}" required><button type="submit">Save name</button></form>{{end}}</li>{{else}}<li class="muted">No recent DMs. Start one below.</li>{{end}}</ul>
  </section>
  <section class="panel" aria-labelledby="new-dm"><h2 id="new-dm">New message</h2><p class="muted">Select one person for a one-to-one DM, or several people for a group DM.</p>
    {{if .CanMessage}}<form method="post" action="/app/conversation/open"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="return" value="dms"><div class="people">{{range .Members}}<label class="person-choice"><input type="checkbox" name="user_{{.ID}}" value="1"><span><strong>{{.Name}}</strong>{{if and .RealName (ne .RealName .Name)}}<span class="muted">{{.RealName}}</span>{{end}}</span></label>{{else}}<p class="muted">No matching active members.</p>{{end}}</div><div class="compose-actions"><label>Group DM name (optional)<input class="group-name" name="name" maxlength="80" placeholder="Design launch"></label><button class="primary" type="submit">Start conversation</button></div></form>{{else}}<p class="muted">Your current permissions do not allow starting direct messages.</p>{{end}}
  </section>
</main>
{{end}}`

var directMessagesTemplate = mustPage(directMessagesMarkup)

const directExpansionReviewMarkup = `{{define "title"}}Add people to a DM · SameOldChat{{end}}
{{define "styles"}}<style>
.review-shell{min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--panel)}
.review-card{width:min(620px,100%);background:var(--panel-strong);border:1px solid var(--line);border-radius:14px;padding:28px;box-shadow:var(--shadow)}
.review-card h1{margin-top:0}.review-card ul{padding-left:22px}.review-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:24px}.review-actions a,.review-actions button{border-radius:7px;padding:10px 14px;font-weight:800}.review-actions a{border:1px solid var(--line);color:var(--text);text-decoration:none}.review-actions button{border:0;background:var(--action);color:var(--on-strong)}
</style>{{end}}
{{define "content"}}
<main class="review-shell">
  {{if .ChooseHistory}}
  <section class="review-card" aria-labelledby="history-dm-heading">
    <h1 id="history-dm-heading">Include conversation history</h1>
    <p>Choose what existing messages and files the people joining <strong>{{.SourceName}}</strong> can see.</p>
    <h2>People being added</h2>
    <ul>{{range .Additions}}<li>{{.Name}}</li>{{end}}</ul>
    <form method="post" action="/app/conversation/add-people?channel={{.SourceID}}">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <input type="hidden" name="stage" value="review">
      {{range .Additions}}<input type="hidden" name="user_{{.ID}}" value="1">{{end}}
      <fieldset>
        <legend>Include conversation history</legend>
        <label class="privacy"><input type="radio" name="history" value="none" checked> Don’t include conversation history</label>
        <label class="privacy"><input type="radio" name="history" value="all"> Include all conversation history and files</label>
      </fieldset>
      <div class="review-actions"><a href="/app?channel={{.SourceID}}&amp;details=1">Cancel</a><button type="submit">Done</button></div>
    </form>
  </section>
  {{else}}
  <section class="review-card" aria-labelledby="review-dm-heading">
    <h1 id="review-dm-heading">Review new group DM</h1>
    <p>Adding these people creates a new group DM. <strong>{{.SourceName}}</strong> keeps its current members and history.</p>
    <h2>People being added</h2>
    <ul>{{range .Additions}}<li>{{.Name}}</li>{{end}}</ul>
    <h2>Conversation history</h2>
    <p>{{if .IncludeHistory}}All existing messages and shared files will also be visible in the new group DM.{{else}}The new group DM will start without existing messages or files.{{end}}</p>
    <p>Slack posts an automatic participant notice in both conversations.</p>
    <form method="post" action="/app/conversation/add-people?channel={{.SourceID}}">
      <input type="hidden" name="_csrf" value="{{.CSRFToken}}">
      <input type="hidden" name="confirm" value="true">
      <input type="hidden" name="history" value="{{.History}}">
      {{range .Additions}}<input type="hidden" name="user_{{.ID}}" value="1">{{end}}
      <div class="review-actions"><a href="/app?channel={{.SourceID}}&amp;details=1">Cancel</a><button type="submit">Confirm and create group DM</button></div>
    </form>
  </section>
  {{end}}
</main>
{{end}}`

var directExpansionReviewTemplate = mustPage(directExpansionReviewMarkup)

const oauthConsentMarkup = `{{define "title"}}Authorize {{.AppName}} · SameOldChat{{end}}
{{define "styles"}}<style>
.oauth-shell{min-height:100vh;display:grid;place-items:center;padding:24px;background:var(--panel)}
.oauth-card{width:min(560px,100%);background:var(--panel-strong);border:1px solid var(--line);border-radius:14px;padding:28px;box-shadow:var(--shadow)}
.oauth-card h1{margin:0 0 8px;font-size:26px}.oauth-card h2{font-size:15px;margin:22px 0 8px}.oauth-card p{color:var(--muted)}
.scope-list{margin:0;padding:0;list-style:none;display:grid;gap:7px}.scope-list li{border:1px solid var(--line);border-radius:7px;padding:9px 11px;background:var(--panel);display:flex;flex-wrap:wrap;align-items:baseline;gap:8px}.scope-token{font-size:12px;color:var(--muted)}
.oauth-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:24px}.oauth-actions button{border-radius:6px;padding:9px 15px;font-weight:800}
.deny{border:1px solid var(--field-line);background:var(--panel-strong);color:var(--text)}.approve{border:0;background:var(--ok);color:var(--on-strong)}
</style>{{end}}
{{define "content"}}<main class="oauth-shell"><section class="oauth-card" aria-labelledby="oauth-title">
<h1 id="oauth-title">Authorize {{.AppName}}</h1><p>This app is asking to access your SameOldChat workspace. Review every permission before continuing.</p>
{{if .BotScopes}}<h2>What the app’s bot can do</h2><ul class="scope-list">{{range .BotScopes}}<li>{{if .Description}}<strong>{{.Description}}</strong> {{end}}<code class="scope-token">{{.Name}}</code></li>{{end}}</ul>{{end}}
{{if .UserScopes}}<h2>What the app can do as you</h2><ul class="scope-list">{{range .UserScopes}}<li>{{if .Description}}<strong>{{.Description}}</strong> {{end}}<code class="scope-token">{{.Name}}</code></li>{{end}}</ul>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="scope" value="{{range $i,$v := .BotScopes}}{{if $i}},{{end}}{{$v.Name}}{{end}}">
<input type="hidden" name="user_scope" value="{{range $i,$v := .UserScopes}}{{if $i}},{{end}}{{$v.Name}}{{end}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
<div class="oauth-actions"><button class="deny" type="submit" name="decision" value="deny">Cancel</button><button class="approve" type="submit" name="decision" value="approve">Allow</button></div>
</form></section></main>{{end}}`

var oauthConsentTemplate = mustPage(oauthConsentMarkup)

const searchMarkup = `{{define "title"}}Search · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}
.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}
.bar form{position:relative;display:flex;flex:1 1 auto;min-width:0;max-width:600px;margin:auto;gap:8px}
.bar input{flex:1 1 auto;min-width:0;border:1px solid #ffffff8a;border-radius:5px;padding:8px 10px;background:#ffffff2b;color:var(--on-accent)}
.bar input::placeholder{color:#ffffffd6}
.bar button{border:1px solid #ffffff6b;background:transparent;color:var(--on-accent);border-radius:5px;padding:6px 10px}
.search-suggestions{position:absolute;z-index:30;top:calc(100% + 6px);left:0;right:0;max-height:min(420px,70vh);overflow:auto;padding:6px;border:1px solid var(--line);border-radius:8px;background:var(--panel-strong);color:var(--text);box-shadow:var(--shadow)}
.search-suggestions[hidden]{display:none}.search-suggestion{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:4px 12px;padding:8px 10px;border-radius:6px;color:var(--text);text-decoration:none}.search-suggestion:hover,.search-suggestion[aria-selected=true]{background:var(--hover)}.search-suggestion-label{min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:700}.search-suggestion-kind{color:var(--muted);font-size:12px;text-transform:capitalize}
.layout{max-width:980px;margin:0 auto;padding:28px 22px}
.heading{border-bottom:1px solid var(--line);padding-bottom:18px;margin-bottom:22px}
.heading h1{margin:0 0 4px;font-size:26px}
.muted{color:var(--muted)}
.search-tabs{display:flex;gap:4px;border-bottom:1px solid var(--line);overflow:auto;margin-bottom:14px}.search-tabs a{padding:9px 13px;color:var(--muted);font-weight:800;text-decoration:none;border-bottom:3px solid transparent;white-space:nowrap}.search-tabs a[aria-current=page]{color:var(--text);border-bottom-color:var(--action)}
.filters{display:grid;grid-template-columns:repeat(6,minmax(110px,1fr));gap:9px;align-items:end;padding:13px;margin-bottom:18px;background:var(--panel);border:1px solid var(--line);border-radius:8px}.filters label{display:grid;gap:4px;font-size:12px;font-weight:800}.filters select,.filters input{min-width:0;width:100%;border:1px solid var(--field-line);border-radius:5px;padding:7px;background:var(--field);color:var(--text)}.filters button{border:0;border-radius:5px;padding:8px 12px;background:var(--action);color:var(--on-strong);font-weight:800}.scope-note{grid-column:1/-1;margin:0;color:var(--muted);font-size:12px}.scope-note a{color:var(--action)}
.results{display:grid;gap:8px}
.result{display:block;padding:14px;background:var(--panel);border:1px solid var(--line);border-radius:8px;color:inherit;text-decoration:none}
.result:hover{border-color:var(--action)}
.author{font-weight:700}
.time{color:var(--muted);font-size:12px;margin-left:8px}
.channel{color:var(--muted);font-size:12px;margin-left:8px}
.text{margin:6px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}
.file-result{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:8px}.file-result .text{color:var(--muted)}.result-kind{color:var(--muted);font-size:12px;font-weight:700}
.empty{color:var(--muted);padding:22px;text-align:center}
.recent-searches{margin-top:24px}.recent-searches h2{font-size:16px}.recent-searches ul{display:grid;gap:6px;margin:0;padding:0;list-style:none}.recent-searches a{display:block;padding:10px 12px;border:1px solid var(--line);border-radius:7px;color:var(--text);text-decoration:none}.recent-searches a:hover{background:var(--hover);border-color:var(--action)}
.pager{text-align:center;margin-top:18px}.pager a{color:var(--action);font-weight:800}
@media(max-width:820px){.filters{grid-template-columns:repeat(2,minmax(0,1fr))}}
@media(max-width:720px){.layout{padding:20px 14px}.bar{padding:0 12px;gap:10px}.filters{grid-template-columns:1fr}.file-result{grid-template-columns:1fr}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + searchSuggestionsScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><form method="get" action="/app/search" role="search" aria-label="Search the workspace"><label class="visually-hidden" for="search-query">Search the workspace</label><input id="search-query" type="search" name="q" maxlength="500" value="{{.Query}}" placeholder="Search messages, files, people, and channels" role="combobox" autocomplete="off" aria-autocomplete="list" aria-haspopup="listbox" aria-expanded="false" required autofocus><button type="submit">Search</button><input type="hidden" name="channel" value="{{.Channel}}"><input type="hidden" name="type" value="{{.Type}}">{{if .CurrentOnly}}<input type="hidden" name="scope" value="channel">{{end}}</form><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout"><div class="heading"><h1>Search results</h1>{{if .Error}}<p class="form-error" role="alert">{{.Error}}</p>{{else if .Searched}}<p class="muted">{{.ResultCount}} results in {{.Type}} for “{{.Query}}”</p>{{else}}<p class="muted">Enter a search term or choose a recent search.</p>{{end}}{{if .Warning}}<p class="notice" role="status">{{.Warning}}</p>{{end}}</div>
{{if and (not .Searched) .Recent}}<section class="recent-searches" aria-labelledby="recent-searches-title"><h2 id="recent-searches-title">Recent searches</h2><ul>{{range .Recent}}<li><a href="{{.URL}}">{{.Query}}</a></li>{{end}}</ul></section>{{end}}
{{if .CurrentOnly}}<p class="scope-note">Searching only this conversation. <a href="/app/search?q={{.Query}}&amp;channel={{.Channel}}&amp;type={{.Type}}">Search the whole workspace</a></p>{{end}}
{{if .Searched}}<nav class="search-tabs" aria-label="Search result types">{{range .Tabs}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>
{{if or (eq .Type "messages") (eq .Type "files")}}<form class="filters" method="get" action="/app/search" aria-label="Search filters"><input type="hidden" name="q" value="{{.Query}}"><input type="hidden" name="channel" value="{{.Channel}}"><input type="hidden" name="type" value="{{.Type}}">
<label>From<select name="from"><option value="">Anyone</option>{{range .MemberOptions}}<option value="{{.ID}}"{{if eq .ID $.SelectedMember}} selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label>In<select name="in"><option value="">Anywhere</option>{{range .ConversationOptions}}<option value="{{.ID}}"{{if eq .ID $.SelectedConversation}} selected{{end}}>{{.Name}}</option>{{end}}</select></label>
<label>After<input type="date" name="after" value="{{.After}}"></label><label>Before<input type="date" name="before" value="{{.Before}}"></label>
<label>Contains<select name="has"><option value="">Anything</option>{{if eq .Type "messages"}}<option value="file"{{if eq .Has "file"}} selected{{end}}>A file</option><option value="pin"{{if eq .Has "pin"}} selected{{end}}>A pin</option><option value="reaction"{{if eq .Has "reaction"}} selected{{end}}>A reaction</option>{{else}}<option value="images"{{if eq .Has "images"}} selected{{end}}>Images</option><option value="pdf"{{if eq .Has "pdf"}} selected{{end}}>PDF files</option><option value="text"{{if eq .Has "text"}} selected{{end}}>Text files</option>{{end}}</select></label>
<label>Sort<select name="order"><option value="relevant"{{if eq .Sort "score"}} selected{{end}}>Most relevant</option><option value="newest"{{if and (eq .Sort "timestamp") (eq .Direction "desc")}} selected{{end}}>Newest</option><option value="oldest"{{if eq .Direction "asc"}} selected{{end}}>Oldest</option></select></label><button type="submit">Apply filters</button>
{{if .CurrentOnly}}<input type="hidden" name="scope" value="channel">{{end}}</form>{{end}}{{end}}
<section class="results" aria-label="{{.Type}} search results">
{{if eq .Type "messages"}}{{range .Messages}}<a class="result" href="{{.Permalink}}"><span class="author">{{.AuthorName}}</span>{{if .AuthorStatus}}<span class="author-status"{{if .AuthorStatusText}} title="{{.AuthorStatusText}}"{{end}}>{{.AuthorStatus}}</span>{{end}}<time class="time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time><span class="channel">{{.ChannelPrefix}}{{.ChannelName}}</span><p class="text">{{.DisplayText}}</p></a>{{else}}{{if $.Searched}}<p class="empty">No matching messages.</p>{{end}}{{end}}
{{else if eq .Type "files"}}{{range .Files}}<a class="result file-result" href="{{.DownloadURL}}"><span><span class="author">{{if .Title}}{{.Title}}{{else}}{{.Name}}{{end}}</span><span class="result-kind">{{.MIMEType}} · {{.Size}}</span><p class="text">Uploaded by {{.Uploader}}</p></span><time class="time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></a>{{else}}<p class="empty">No matching files.</p>{{end}}
{{else if eq .Type "canvases"}}{{range .Canvases}}<a class="result canvas-result" href="{{.URL}}"><span><span class="author">{{.Title}}</span><span class="result-kind">Canvas · {{.Owner}}</span>{{if .Snippet}}<p class="text">{{.Snippet}}</p>{{end}}</span><time class="time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></a>{{else}}<p class="empty">No matching canvases.</p>{{end}}
{{else if eq .Type "lists"}}{{range .Lists}}<a class="result list-result" href="{{.URL}}"><span><span class="author">{{.Title}}</span><span class="result-kind">List · {{.Owner}}</span>{{if .Snippet}}<p class="text">{{.Snippet}}</p>{{end}}</span><time class="time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></a>{{else}}<p class="empty">No matching lists.</p>{{end}}
{{else if eq .Type "people"}}{{range .People}}<a class="result" href="/app/members?user={{.ID}}"><span class="author">{{if .MarkedName}}{{.MarkedName}}{{else}}{{.Name}}{{end}}</span>{{if .RealName}}<p class="text">{{.RealName}}</p>{{end}}</a>{{else}}<p class="empty">No matching people.</p>{{end}}
{{else}}{{range .Conversations}}<a class="result" href="/app?channel={{.ID}}"><span class="author"># {{if .MarkedName}}{{.MarkedName}}{{else}}{{.Name}}{{end}}</span></a>{{else}}<p class="empty">No matching channels.</p>{{end}}{{end}}
</section>{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more results</a></p>{{end}}</main>{{end}}`

var searchTemplate = mustPage(searchMarkup)

var activityMarkup = `{{define "title"}}Activity · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(980px,calc(100% - 28px));margin:22px auto 48px}.activity-heading{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin-bottom:12px}.activity-heading h2{margin:0 auto 0 0;font-size:25px}.layout-forms{display:flex;gap:4px}.layout-forms form{margin:0}.layout-forms button,.bulk-actions button,.item-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:7px 10px;font-weight:800}.layout-forms button[aria-pressed=true]{background:var(--action);color:var(--on-strong)}
.activity-tabs{display:flex;gap:3px;overflow-x:auto;border-bottom:1px solid var(--line);margin-bottom:10px}.activity-tabs a{white-space:nowrap;padding:9px 11px;color:var(--muted);font-weight:800;text-decoration:none;border-bottom:3px solid transparent}.activity-tabs a[aria-current=page]{color:var(--text);border-bottom-color:var(--action)}.activity-options{display:flex;gap:8px;flex-wrap:wrap;margin:0 0 10px}.activity-options a{border:1px solid var(--field-line);border-radius:18px;padding:5px 10px;color:var(--text);text-decoration:none;font-weight:700}.activity-options a[aria-current=page]{background:var(--hover);border-color:var(--action)}
.saved-views{display:flex;flex-wrap:wrap;align-items:flex-start;gap:10px;margin:0 0 10px}.saved-view-delete button,.saved-view-create summary{border:1px solid var(--field-line);border-radius:18px;padding:5px 10px;background:var(--bg);color:var(--text);font-weight:700;cursor:pointer;list-style:none}.saved-view-create[open] summary{background:var(--hover);border-color:var(--action)}.saved-view-create form{margin-top:8px;display:grid;gap:8px;border:1px solid var(--line);border-radius:8px;padding:10px;background:var(--panel);max-width:320px}.saved-view-name{display:grid;gap:4px;font-weight:700}.saved-view-name input{padding:7px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.saved-view-kinds{display:grid;grid-template-columns:1fr 1fr;gap:4px 12px;border:1px solid var(--line);border-radius:6px;margin:0;padding:8px}.saved-view-kinds legend{font-weight:700;color:var(--muted);padding:0 4px}.saved-view-kinds label{display:flex;align-items:center;gap:6px;font-weight:400}.saved-view-create button[type=submit]{justify-self:start;border:0;border-radius:6px;padding:8px 12px;background:var(--action);color:var(--on-strong);font-weight:800}
.bulk-actions{min-height:39px;display:flex;align-items:center;gap:7px;padding:7px 10px;border:1px solid var(--line);border-radius:8px 8px 0 0;background:var(--panel)}.bulk-actions span{margin-right:auto;color:var(--muted);font-size:13px}.activity-list{margin:0;padding:0;list-style:none;border:1px solid var(--line);border-top:0;border-radius:0 0 8px 8px;overflow:hidden}.activity-row{display:grid;grid-template-columns:auto minmax(0,1fr) auto;gap:11px;padding:14px;background:var(--panel);border-top:1px solid var(--line);outline:none}.activity-row:first-child{border-top:0}.activity-row:hover,.activity-row:focus{background:var(--hover)}.activity-row.unread{box-shadow:inset 3px 0 var(--action)}.activity-select{margin-top:3px}.activity-main{min-width:0}.activity-head{display:flex;align-items:baseline;gap:7px;flex-wrap:wrap}.activity-kind{font-size:11px;font-weight:900;text-transform:uppercase;letter-spacing:.04em;color:var(--action)}.activity-author{font-weight:800}.activity-meta{color:var(--muted);font-size:12px}.activity-text{margin:6px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}.activity-source{color:inherit;text-decoration:none}.activity-source:focus-visible{text-decoration:underline}.item-actions{display:flex;gap:4px;align-items:start}.item-actions button{padding:5px 8px}.unavailable{color:var(--muted);font-style:italic}.empty{margin:0;padding:34px;border:1px dashed var(--line);border-radius:8px;color:var(--muted);text-align:center}.pager{text-align:center;margin-top:16px}
.activity-list.dense .activity-row{padding:8px 12px}.activity-list.dense .activity-text{display:inline;margin-left:5px}.activity-list.dense .activity-head{display:inline-flex}
.activity-reaction-dialog{width:min(520px,calc(100% - 24px));border:1px solid var(--line);border-radius:10px;background:var(--panel);color:var(--text);padding:0;box-shadow:0 18px 55px #0005}.activity-reaction-dialog::backdrop{background:#0007}.activity-reaction-shell{padding:16px}.activity-reaction-head{display:flex;align-items:center;gap:10px}.activity-reaction-head h3{margin:0 auto 0 0}.activity-reaction-head button{border:0;background:transparent;color:var(--text);font-size:22px}.activity-reaction-controls{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:8px;margin:13px 0}.activity-reaction-controls input,.activity-reaction-controls select{min-width:0;padding:8px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.activity-reaction-results{display:grid;grid-template-columns:repeat(auto-fill,minmax(68px,1fr));gap:5px;margin:0;padding:0;list-style:none;max-height:310px;overflow:auto}.activity-reaction-results button{width:100%;min-height:58px;border:1px solid transparent;border-radius:7px;background:transparent;color:var(--text);display:grid;place-items:center;padding:4px}.activity-reaction-results button:hover,.activity-reaction-results button:focus{background:var(--hover);border-color:var(--action)}.activity-reaction-results img{width:24px;height:24px;object-fit:contain}.activity-reaction-results small{max-width:100%;overflow:hidden;text-overflow:ellipsis}.activity-reaction-status{min-height:20px;color:var(--muted);font-size:13px}
@media(max-width:650px){.bar{padding:0 12px}.layout{width:min(100% - 16px,980px);margin-top:14px}.activity-row{grid-template-columns:auto minmax(0,1fr)}.item-actions{grid-column:2}.bulk-actions{overflow-x:auto}.activity-list.dense .activity-text{display:block;margin:4px 0 0}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `<script>(function(){
var activityTopics=` + liveEventTopicsLiteral() + `.concat(['later_reminder.delivered','later_reminder.failed']);
var feed=document.getElementById('activity-feed');var liveStatus=document.getElementById('activity-live-status');var rows=[];var current=0;var refreshing=false;var refreshQueued=false;var refreshTimer=0;
function syncRows(preferredID){rows=Array.prototype.slice.call(document.querySelectorAll('[data-activity-row]'));current=0;if(preferredID){var preferred=rows.findIndex(function(row){return row.getAttribute('data-activity-id')===preferredID});if(preferred>=0)current=preferred}rows.forEach(function(row,index){row.tabIndex=index===current?0:-1})}
syncRows('');
function focusRow(index){if(!rows.length)return;rows[current].tabIndex=-1;current=(index+rows.length)%rows.length;rows[current].tabIndex=0;rows[current].focus()}
document.addEventListener('keydown',function(event){if(event.altKey||event.ctrlKey||event.metaKey||event.target.matches('input,textarea,select'))return;var row=event.target.closest('[data-activity-row]');if(row)current=rows.indexOf(row);
if(event.key==='ArrowDown'){event.preventDefault();focusRow(current+1)}else if(event.key==='ArrowUp'){event.preventDefault();focusRow(current-1)}else if(event.key==='Enter'&&row){var reply=row.querySelector('[data-activity-reply]');if(reply){event.preventDefault();reply.click()}}else if((event.key==='x'||event.key==='X')&&row){event.preventDefault();var box=row.querySelector('input[type=checkbox]');box.checked=!box.checked;box.dispatchEvent(new Event('change',{bubbles:true}))}else if((event.key==='c'||event.key==='C')&&row){event.preventDefault();row.querySelector('[data-clear-button]').click()}else if((event.key==='r'||event.key==='R')&&row){var read=row.querySelector('[data-read-button]');if(read){event.preventDefault();read.click()}}});
var count=document.getElementById('selection-count');document.addEventListener('change',function(){if(!count)return;var selected=document.querySelectorAll('input[name=activity_id]:checked').length;count.textContent=selected?selected+' selected':'Select items with X or the checkboxes'});
var dialog=document.getElementById('activity-reaction-dialog');var form=document.getElementById('activity-reaction-form');var search=document.getElementById('activity-reaction-search');var category=document.getElementById('activity-reaction-category');var tone=document.getElementById('activity-reaction-tone');var results=document.getElementById('activity-reaction-results');var status=document.getElementById('activity-reaction-status');var returnFocus=null;var request=null;var timer=0;
function recentEmoji(){try{var value=JSON.parse(localStorage.getItem('sameoldchat-recent-emoji')||'[]');return Array.isArray(value)?value:[]}catch(error){return[]}}
function rememberEmoji(name){var values=recentEmoji().filter(function(value){return value!==name});values.unshift(name);try{localStorage.setItem('sameoldchat-recent-emoji',JSON.stringify(values.slice(0,24)))}catch(error){}}
function closeReaction(){if(dialog&&dialog.open)dialog.close();if(returnFocus)returnFocus.focus()}
function loadReactions(){if(!results)return;if(request)request.abort();request=window.AbortController?new AbortController():null;status.textContent='Loading emoji…';var parameters=new URLSearchParams({q:search.value||''});if(category.value)parameters.set('category',category.value);var recent=recentEmoji();if(recent.length)parameters.set('recent',recent.slice(0,24).join(','));fetch('/app/emoji/options?'+parameters.toString(),{credentials:'same-origin',signal:request?request.signal:undefined}).then(function(response){if(!response.ok)throw new Error();return response.json()}).then(function(payload){results.textContent='';var options=payload&&Array.isArray(payload.options)?payload.options:[];if(payload&&Array.isArray(payload.categories)&&category.options.length<=1){payload.categories.forEach(function(name){var option=document.createElement('option');option.value=name;option.textContent=name;category.appendChild(option)})}options.forEach(function(option){var item=document.createElement('li');var button=document.createElement('button');button.type='button';button.setAttribute('data-activity-emoji',option.name);button.setAttribute('data-skin-tones',option.skin_tones?'true':'false');button.setAttribute('aria-label','React with :'+option.name+':');var visual;if(option.image_url){visual=document.createElement('img');visual.src=option.image_url;visual.alt=''}else{visual=document.createElement('span');visual.textContent=option.display||''}var label=document.createElement('small');label.textContent=':'+option.name+':';button.appendChild(visual);button.appendChild(label);item.appendChild(button);results.appendChild(item)});status.textContent=options.length?options.length+' emoji shown.':'No matching emoji.'}).catch(function(error){if(error&&error.name==='AbortError')return;results.textContent='';status.textContent='Emoji could not be loaded. Try again.'})}
document.addEventListener('click',function(event){var open=event.target.closest('[data-activity-react]');if(open){returnFocus=open;form.action=open.getAttribute('data-activity-react');dialog.showModal();search.value='';category.value='';search.focus();loadReactions();return}var choice=event.target.closest('[data-activity-emoji]');if(choice){var name=choice.getAttribute('data-activity-emoji');var selectedTone=tone.value;if(selectedTone&&choice.getAttribute('data-skin-tones')==='true')name+='::skin-tone-'+selectedTone;var body=new FormData(form);body.set('name',name);choice.disabled=true;status.textContent='Adding reaction…';fetch(form.action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error();rememberEmoji(choice.getAttribute('data-activity-emoji'));closeReaction()}).catch(function(){choice.disabled=false;status.textContent='The reaction was not added. Try again.'})}});
if(search)search.addEventListener('input',function(){window.clearTimeout(timer);timer=window.setTimeout(loadReactions,120)});
if(category)category.addEventListener('change',loadReactions);
var close=document.getElementById('activity-reaction-close');if(close)close.addEventListener('click',closeReaction);
if(dialog)dialog.addEventListener('click',function(event){if(event.target===dialog)closeReaction()});
function refreshActivity(){
if(!feed)return;if(refreshing){refreshQueued=true;return}refreshing=true;
var active=document.activeElement;var focusedRow=active&&active.closest?active.closest('[data-activity-row]'):null;var focusedID=focusedRow?focusedRow.getAttribute('data-activity-id'):'';var focusedLabel=active&&active.getAttribute?active.getAttribute('aria-label')||'':'';var selected={};Array.prototype.forEach.call(feed.querySelectorAll('input[name=activity_id]:checked'),function(input){selected[input.value]=true});var scrollX=window.scrollX;var scrollY=window.scrollY;
fetch(window.location.pathname+window.location.search,{headers:{'X-SameOldChat-Activity-Refresh':'true'},credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error();return response.text()}).then(function(html){var parsed=new DOMParser().parseFromString(html,'text/html');var replacement=parsed.getElementById('activity-feed');if(!replacement)throw new Error();feed.replaceWith(replacement);feed=replacement;Object.keys(selected).forEach(function(id){Array.prototype.forEach.call(feed.querySelectorAll('input[name=activity_id]'),function(input){if(input.value===id)input.checked=true})});syncRows(focusedID);if(focusedID){var row=rows[current];var target=row;if(focusedLabel){Array.prototype.some.call(row.querySelectorAll('[aria-label]'),function(candidate){if(candidate.getAttribute('aria-label')===focusedLabel){target=candidate;return true}return false})}target.focus({preventScroll:true});window.scrollTo(scrollX,scrollY)}if(liveStatus)liveStatus.textContent='Activity updated.'}).catch(function(){if(liveStatus)liveStatus.textContent='New activity is available. Reload to update the list.'}).finally(function(){refreshing=false;if(refreshQueued){refreshQueued=false;refreshActivity()}});
}
function scheduleActivityRefresh(){window.clearTimeout(refreshTimer);refreshTimer=window.setTimeout(refreshActivity,180)}
if(window.EventSource){var cursor='';try{cursor=sessionStorage.getItem('sameoldchat-last-event')||''}catch(error){}var stream=new EventSource('/events'+(cursor?'?last_event_id='+encodeURIComponent(cursor):''));activityTopics.forEach(function(topic){stream.addEventListener(topic,function(event){if(event.lastEventId){try{sessionStorage.setItem('sameoldchat-last-event',event.lastEventId)}catch(error){}}scheduleActivityRefresh()})});stream.onerror=function(){if(liveStatus)liveStatus.textContent='Reconnecting to live Activity…'};stream.onopen=function(){if(liveStatus&&liveStatus.textContent==='Reconnecting to live Activity…')liveStatus.textContent='Live Activity resumed.'}}
})();</script>{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Activity</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<div class="activity-heading"><h2>Activity</h2><span class="visually-hidden" id="activity-live-status" role="status" aria-live="polite"></span><div class="layout-forms" aria-label="Activity layout"><form method="post" action="/app/activity/preferences?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="kind" value="{{.Kind}}"><input type="hidden" name="view" value="{{.View}}"><input type="hidden" name="unread" value="{{if .UnreadOnly}}1{{end}}"><input type="hidden" name="cleared" value="{{if .ClearedOnly}}1{{end}}"><input type="hidden" name="layout" value="detailed"><button type="submit" aria-pressed="{{if eq .Layout "detailed"}}true{{else}}false{{end}}">Detailed</button></form><form method="post" action="/app/activity/preferences?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="kind" value="{{.Kind}}"><input type="hidden" name="view" value="{{.View}}"><input type="hidden" name="unread" value="{{if .UnreadOnly}}1{{end}}"><input type="hidden" name="cleared" value="{{if .ClearedOnly}}1{{end}}"><input type="hidden" name="layout" value="dense"><button type="submit" aria-pressed="{{if eq .Layout "dense"}}true{{else}}false{{end}}">Dense</button></form></div></div>
<nav class="activity-tabs" aria-label="Activity filters">{{range .Filters}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}{{range .SavedViews}}<a href="{{.URL}}"{{if .Current}} aria-current="page"{{end}}>{{.Name}}</a>{{end}}</nav>
<div class="activity-options"><a href="{{.UnreadURL}}"{{if .UnreadOnly}} aria-current="page"{{end}}>Unread</a>{{if .ClearedOnly}}<a href="{{.ActiveURL}}">Back to activity</a>{{else}}<a href="{{.ClearedURL}}">Cleared</a>{{end}}</div>
<div class="saved-views">{{if .View}}<form class="saved-view-delete" method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="view_id" value="{{.View}}"><button type="submit">Delete this view</button></form>{{end}}<details class="saved-view-create"><summary>New saved view</summary><form method="post" action="{{.CreateURL}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="saved-view-name">Name<input name="name" maxlength="80" placeholder="Important" required></label><fieldset class="saved-view-kinds"><legend>Kinds</legend>{{range .KindOptions}}<label><input type="checkbox" name="kind" value="{{.Value}}">{{.Label}}</label>{{end}}</fieldset><button type="submit">Save view</button></form></details></div>
<div id="activity-feed">
<form method="post" action="/app/activity/mutate?channel={{.Channel}}">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="kind" value="{{.Kind}}"><input type="hidden" name="view" value="{{.View}}"><input type="hidden" name="unread" value="{{if .UnreadOnly}}1{{end}}"><input type="hidden" name="cleared" value="{{if .ClearedOnly}}1{{end}}">
<div class="bulk-actions"><span id="selection-count" aria-live="polite">Select items with X or the checkboxes</span>{{if .ClearedOnly}}<button type="submit" name="mutation" value="restore">Restore selected</button>{{else}}<button type="submit" name="mutation" value="read">Mark selected read</button><button type="submit" name="mutation" value="unread">Mark selected unread</button><button type="submit" name="mutation" value="clear">Clear selected</button>{{end}}</div>
{{if .Items}}<ul class="activity-list {{.Layout}}" aria-label="Activity feed">{{range .Items}}<li class="activity-row{{if .Unread}} unread{{end}}" data-activity-row data-activity-id="{{.ID}}" tabindex="-1">
<input class="activity-select" type="checkbox" name="activity_id" value="{{.ID}}" aria-label="Select activity from {{.ActorName}}">
<div class="activity-main">{{if .ReplyURL}}<a class="visually-hidden" data-activity-reply href="{{.ReplyURL}}">Reply to this activity</a>{{end}}{{if .SourceURL}}<a class="activity-source" data-activity-source href="{{.SourceURL}}">{{end}}<span class="activity-head"><span class="activity-kind">{{.KindLabel}}</span>{{if .ActorName}}<span class="activity-author">{{.ActorName}}</span>{{end}}<time class="activity-meta" datetime="{{.MachineTime}}">{{.DisplayTime}}</time>{{if .ChannelName}}<span class="activity-meta">{{.ChannelName}}</span>{{end}}</span><span class="activity-text{{if .Unavailable}} unavailable{{end}}">{{.Text}}</span>{{if .SourceURL}}</a>{{end}}</div>
<div class="item-actions">{{if .ReactionURL}}<button type="button" data-activity-react="{{.ReactionURL}}" aria-label="Add a reaction to this message">React</button>{{end}}{{if $.ClearedOnly}}<button type="submit" name="single_id" value="{{.ID}}" formaction="/app/activity/mutate?channel={{$.Channel}}&mutation=restore" data-clear-button aria-label="Restore this activity">Restore</button>{{else}}{{if .Unread}}<button type="submit" name="single_id" value="{{.ID}}" formaction="/app/activity/mutate?channel={{$.Channel}}&mutation=read" data-read-button aria-label="Mark this activity read">Read</button>{{else}}<button type="submit" name="single_id" value="{{.ID}}" formaction="/app/activity/mutate?channel={{$.Channel}}&mutation=unread" aria-label="Mark this activity unread">Unread</button>{{end}}<button type="submit" name="single_id" value="{{.ID}}" formaction="/app/activity/mutate?channel={{$.Channel}}&mutation=clear" data-clear-button aria-label="Clear this activity">Clear</button>{{end}}</div>
</li>{{end}}</ul>{{else}}<p class="empty">{{if .ClearedOnly}}No cleared activity.{{else if .UnreadOnly}}You’re all caught up.{{else}}No activity yet. New DMs, mentions, thread replies, reactions, invitations, app messages, and delivered reminders will appear here.{{end}}</p>{{end}}
</form>{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more activity</a></p>{{end}}</div>
<dialog class="activity-reaction-dialog" id="activity-reaction-dialog" aria-labelledby="activity-reaction-heading"><div class="activity-reaction-shell"><div class="activity-reaction-head"><h3 id="activity-reaction-heading">Add reaction</h3><button id="activity-reaction-close" type="button" aria-label="Close reaction picker">×</button></div><form id="activity-reaction-form" method="post"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><div class="activity-reaction-controls"><label class="visually-hidden" for="activity-reaction-search">Search emoji</label><input id="activity-reaction-search" type="search" autocomplete="off" placeholder="Search emoji"><label class="visually-hidden" for="activity-reaction-category">Category</label><select id="activity-reaction-category"><option value="">All categories</option></select><label class="visually-hidden" for="activity-reaction-tone">Skin tone</label><select id="activity-reaction-tone"><option value="">Default tone</option><option value="2">Skin tone 2</option><option value="3">Skin tone 3</option><option value="4">Skin tone 4</option><option value="5">Skin tone 5</option><option value="6">Skin tone 6</option></select></div><p class="activity-reaction-status" id="activity-reaction-status" role="status"></p><ul class="activity-reaction-results" id="activity-reaction-results" aria-label="Emoji results"></ul><noscript><p>Open the message to add a reaction.</p></noscript></form></div></dialog>
</main>{{end}}`

var activityTemplate = mustPage(activityMarkup)

// browserNotificationSettingScript reports what the browser says, because the
// server cannot know it: a stored preference and a granted permission are two
// different things, and one silent "off" for both leaves a person unable to
// tell whether to change a setting here or in their browser.
//
// Turning the preference on asks for the permission then and there, which is
// the only moment a browser will honour the request — it must follow a click.
const browserNotificationSettingScript = `<script>(function(){
var toggle=document.getElementById('browser-notifications');
var state=document.getElementById('browser-notification-state');
if(!toggle||!state)return;
function describe(){
if(!('Notification' in window)){state.textContent='This browser cannot show desktop notifications at all.';toggle.disabled=true;return}
var permission=Notification.permission;
if(permission==='denied'){state.textContent='Your browser is blocking notifications for this site. Turning this on here will not be enough until you allow them in the browser.';return}
if(permission==='granted'){state.textContent=toggle.checked?'Your browser allows notifications, and this workspace will send them while a tab is open.':'Your browser allows notifications. Turn this on to receive them.';return}
state.textContent=toggle.checked?'Your browser has not been asked yet. Save, and it will ask.':'Your browser has not been asked to allow notifications yet.';
}
describe();
toggle.addEventListener('change',function(){
if(toggle.checked&&'Notification' in window&&Notification.permission==='default'){Notification.requestPermission().then(describe).catch(describe);return}
describe();
});
})();</script>`

const notificationsMarkup = `{{define "title"}}Notifications · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(760px,calc(100% - 28px));margin:24px auto 48px}.heading h2{margin:0 0 5px}.heading p{margin:0;color:var(--muted)}.settings{display:grid;gap:18px;margin-top:20px}.card{padding:18px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.card h3{margin:0 0 6px}.card>p{margin:0 0 14px;color:var(--muted)}.fields{display:grid;gap:12px}.fields label{display:grid;gap:6px;font-weight:700}.fields input[type=text],.fields input[type=number],.fields select{padding:9px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.check{display:flex!important;grid-template-columns:auto 1fr!important;align-items:start;gap:8px!important;font-weight:600!important}.actions{display:flex;gap:8px;align-items:end;flex-wrap:wrap}.actions label{flex:1 1 180px}.actions button,.fields button{border:0;border-radius:6px;background:var(--action);color:var(--on-strong);padding:9px 12px;font-weight:800}.resume{background:var(--danger)!important}.exceptions{margin:0;padding:0;list-style:none;display:grid;gap:8px}.exceptions a{display:flex;justify-content:space-between;gap:10px;padding:11px;border:1px solid var(--line);border-radius:7px;color:var(--text);text-decoration:none}.exceptions span:last-child{color:var(--muted)}
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 18px,760px);margin-top:16px}.card{padding:14px}.actions{display:grid}.actions label{width:100%}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + browserNotificationSettingScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Notifications</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header>
<main class="layout"><div class="heading"><h2>Notification preferences</h2><p>Choose what needs your attention without changing what you can read.</p></div>{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<div class="settings">
<section class="card" aria-labelledby="workspace-notifications-heading"><h3 id="workspace-notifications-heading">Workspace defaults</h3><p>Conversation exceptions override this trigger.</p>
<form class="fields" method="post" action="/app/notifications/preferences?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label for="workspace-notification-level">Notify me about<select id="workspace-notification-level" name="level"><option value="mentions"{{if eq .Level "mentions"}} selected{{end}}>Mentions and direct messages</option><option value="all"{{if eq .Level "all"}} selected{{end}}>All new messages</option></select></label>
<label for="notification-keywords">Channel keywords<input id="notification-keywords" type="text" name="keywords" maxlength="5049" value="{{.Keywords}}" placeholder="release, customer escalation"><span class="muted">Comma-separated; exact matches are case-insensitive and do not trigger in threads.</span></label>
<label class="check"><input type="checkbox" name="activity_channels" value="true"{{if .ActivityChannels}} checked{{end}}> Show channels set to All new posts in Activity</label>
<label class="check"><input type="checkbox" name="activity_reminders" value="true"{{if .ActivityReminders}} checked{{end}}> Show due personal reminders in Activity</label>
<label class="check"><input type="checkbox" id="browser-notifications" name="browser_notifications" value="true"{{if .BrowserNotifications}} checked{{end}}> Show desktop notifications while SameOldChat is open in a tab</label>
<p class="muted" id="browser-notification-state" aria-live="polite">{{.BrowserNotificationState}}</p>
<button type="submit">Save workspace defaults</button></form></section>
<section class="card" aria-labelledby="notification-absent-heading"><h3 id="notification-absent-heading">Not delivered here</h3><p>These are absent rather than off, so you know to look elsewhere for them.</p><ul><li><strong>Push to a phone.</strong> There is no mobile application and no push service.</li><li><strong>E-mail.</strong> This deployment sends no mail at all.</li><li><strong>Sounds and notification schedules.</strong> Pausing above is the only schedule.</li></ul></section>
<section class="card" aria-labelledby="schedule-heading"><h3 id="schedule-heading">Notification schedule</h3>
<p>Choose the days and hours you allow notifications. Outside them nothing is delivered; Activity and messages are unaffected.</p>
{{if .ScheduleSuppressing}}<p class="notice" role="status">Right now you are outside your schedule, so notifications are not being delivered.</p>{{end}}
<form method="post" action="/app/notifications/schedule?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<input type="hidden" name="timezone" data-browser-timezone value="UTC">
<label><span><input type="checkbox" name="enabled" value="true"{{if .ScheduleEnabled}} checked{{end}}> Only notify me during these hours</span></label>
<fieldset class="schedule-days"><legend>Days</legend>{{range .ScheduleDays}}<label><span><input type="checkbox" name="day" value="{{.Number}}"{{if .Selected}} checked{{end}}> {{.Label}}</span></label>{{end}}</fieldset>
<label for="schedule-start">From<input id="schedule-start" type="time" name="start" value="{{.ScheduleStart}}" required></label>
<label for="schedule-end">To<input id="schedule-end" type="time" name="end" value="{{.ScheduleEnd}}" required></label>
{{if .ScheduleZone}}<p class="muted">Times are read in {{.ScheduleZone}}.</p>{{end}}
<button type="submit">Save schedule</button></form>
<p class="read-only">A window ending before it starts runs overnight, and belongs to the day it began on.</p>
</section>
<section class="card" aria-labelledby="pause-notifications-heading"><h3 id="pause-notifications-heading">Pause notifications</h3>{{if .Snoozed}}<p>Paused until <time datetime="{{.SnoozeUntil}}">{{.SnoozeUntil}}</time>. Messages and Activity remain available.</p><form method="post" action="/app/notifications/dnd?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="action" value="resume"><button class="resume" type="submit">Resume notifications</button></form>{{else}}<p>Pause banners and sounds for a preset or custom duration.</p><form class="actions" method="post" action="/app/notifications/dnd?channel={{.Channel}}"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="action" value="pause"><label for="dnd-minutes">Preset<select id="dnd-minutes" name="minutes"><option value="30">30 minutes</option><option value="60">1 hour</option><option value="120">2 hours</option><option value="480">8 hours</option><option value="1440">24 hours</option></select></label><label for="dnd-custom-minutes">Custom minutes (optional)<input id="dnd-custom-minutes" type="number" name="custom_minutes" min="1" max="1440"></label><button type="submit">Pause notifications</button></form>{{end}}</section>
<section class="card" aria-labelledby="notification-exceptions-heading"><h3 id="notification-exceptions-heading">Exceptions to defaults</h3>{{if .Exceptions}}<ul class="exceptions">{{range .Exceptions}}<li><a href="{{.URL}}"><span>{{.Prefix}}{{.Name}}</span><span>{{.Level}}{{if .FollowEveryThread}} · following every thread{{end}}</span></a></li>{{end}}</ul>{{else}}<p>No conversation-specific exceptions.</p>{{end}}</section>
</div></main>{{end}}`

var notificationsTemplate = mustPage(notificationsMarkup)

const laterMarkup = `{{define "title"}}Later · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{display:grid;gap:5px;margin-bottom:17px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted)}
.later-tabs{display:flex;gap:4px;border-bottom:1px solid var(--line);margin-bottom:18px}.later-tabs a{padding:10px 13px;color:var(--muted);font-weight:800;text-decoration:none;border-bottom:3px solid transparent}.later-tabs a[aria-current=page]{color:var(--text);border-bottom-color:var(--action)}.later-tabs a:hover{color:var(--text);background:var(--hover)}
.later-list{display:grid;gap:10px;margin:0;padding:0;list-style:none}.later-item{display:grid;gap:11px;padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.later-source{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap}.later-source a{font-weight:800;color:var(--text);text-decoration:none}.later-source a:hover{color:var(--action)}.later-meta{color:var(--muted);font-size:12px}.later-text{margin:0;white-space:pre-wrap;overflow-wrap:anywhere}.later-unavailable{margin:0;color:var(--muted);font-weight:700}.later-actions{display:flex;gap:7px;flex-wrap:wrap}.later-actions form{margin:0}.later-actions button{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:7px 10px;font-weight:800}.later-actions button:hover{background:var(--hover)}.later-actions .remove{color:var(--danger)}.empty{padding:30px;border:1px dashed var(--line);border-radius:10px;color:var(--muted);text-align:center}.pager{text-align:center;margin-top:18px}
.reminder-create{margin:0 0 18px;padding:14px;border:1px solid var(--line);border-radius:10px;background:var(--panel)}.reminder-create summary{font-weight:800;cursor:pointer}.reminder-fields,.reminder-edit{display:grid;grid-template-columns:minmax(0,2fr) minmax(130px,1fr) minmax(110px,1fr);gap:10px;margin-top:12px}.reminder-fields label,.reminder-edit label{display:grid;gap:5px;color:var(--muted);font-size:12px;font-weight:700}.reminder-fields input,.reminder-fields select,.reminder-edit input,.reminder-edit select{min-width:0;padding:8px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.reminder-fields button,.reminder-edit button{align-self:end;padding:9px 12px;border:0;border-radius:6px;background:var(--action);color:var(--on-strong);font-weight:800}.reminder-heading{margin:22px 0 10px;font-size:18px}.reminder-status{font-weight:800;color:var(--muted)}.reminder-status.failed{color:var(--danger)}
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 20px,900px);margin-top:18px}.later-tabs{overflow-x:auto}.later-actions{display:grid;grid-template-columns:1fr 1fr}.later-actions button{width:100%}.reminder-fields,.reminder-edit{grid-template-columns:minmax(0,1fr)}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + laterLiveScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Later</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Later</h2><p>Saved messages and personal reminders are private to you.</p></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if not .ChannelReminders}}<details class="reminder-create"><summary>Add a reminder</summary><form class="reminder-fields" method="post" action="/app/reminders/create?channel={{.Channel}}">
<input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="UTC">
<label>Description<input name="text" maxlength="3000" required></label><label>Date<input type="date" name="date" required></label><label>Time (defaults to 9:00 AM)<input type="time" name="time"></label>
<label>Repeat<select name="recurrence"><option value="">Does not repeat</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="monthly">Monthly</option><option value="yearly">Yearly</option></select></label><button type="submit">Create reminder</button></form></details>{{end}}
<nav class="later-tabs" aria-label="Later sections"><a href="/app/later?channel={{.Channel}}&state=in_progress"{{if and .InProgressCurrent (not .RemindersOnly)}} aria-current="page"{{end}}>In progress</a><a href="/app/later?channel={{.Channel}}&state=archived"{{if .ArchivedCurrent}} aria-current="page"{{end}}>Archived</a><a href="/app/later?channel={{.Channel}}&state=completed"{{if .CompletedCurrent}} aria-current="page"{{end}}>Completed</a><a href="/app/later?channel={{.Channel}}&filter=reminders"{{if and .RemindersOnly (not .ChannelReminders)}} aria-current="page"{{end}}>Upcoming reminders</a><a href="/app/later?channel={{.Channel}}&filter=channel-reminders"{{if .ChannelReminders}} aria-current="page"{{end}}>Channel reminders</a></nav>
{{if or .Reminders .RemindersOnly}}<h3 class="reminder-heading">{{if .ChannelReminders}}Channel reminders you created{{else if .CompletedCurrent}}Completed reminders{{else}}Reminders{{end}}</h3><ul class="later-list" aria-label="{{if .ChannelReminders}}Channel reminders{{else}}Personal reminders{{end}}">{{range .Reminders}}<li class="later-item">
<div class="later-source"><strong>Reminder</strong><span class="later-meta"><time datetime="{{.MachineTime}}">{{.DisplayTime}}</time>{{if .Recurrence}} · Repeats {{.Recurrence}}{{end}}</span></div><p class="later-text">{{.Text}}</p>
{{if .SourceURL}}<a href="{{.SourceURL}}">{{.SourceLabel}}</a>{{end}}{{if .Failed}}<span class="reminder-status failed">Delivery failed: {{.FailureCode}}</span>{{else if .Completed}}<span class="reminder-status">Completed</span>{{else if .Delivered}}<span class="reminder-status">Delivered</span>{{end}}
<div class="later-actions">{{if .CanComplete}}<form method="post" action="{{.CompleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Mark complete</button></form>{{end}}{{if .CanEdit}}<details><summary>Edit</summary><form class="reminder-edit" method="post" action="{{.UpdateURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="timezone" data-browser-timezone value="{{.TimeZone}}"><label>Description<input name="text" maxlength="3000" value="{{.Text}}" required></label><label>Date<input type="date" name="date" value="{{.DateValue}}" required></label><label>Time<input type="time" name="time" value="{{.TimeValue}}" required></label><label>Repeat<select name="recurrence"><option value="">Does not repeat</option><option value="daily"{{if eq .Recurrence "daily"}} selected{{end}}>Daily</option><option value="weekly"{{if eq .Recurrence "weekly"}} selected{{end}}>Weekly</option><option value="monthly"{{if eq .Recurrence "monthly"}} selected{{end}}>Monthly</option><option value="yearly"{{if eq .Recurrence "yearly"}} selected{{end}}>Yearly</option></select></label><button type="submit">Save changes</button></form></details>{{end}}<form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="remove" type="submit">Delete reminder</button></form></div>
</li>{{else}}<li class="empty">{{if $.ChannelReminders}}You have not created any channel reminders.{{else}}You have no upcoming reminders.{{end}}</li>{{end}}</ul>{{end}}
{{if not .RemindersOnly}}
<ul class="later-list" aria-label="{{.StateTitle}} saved items">{{range .Items}}<li class="later-item">
{{if .SourceAvailable}}<div class="later-source"><a href="{{.SourceURL}}">{{.ChannelPrefix}}{{.ChannelName}}</a><span class="later-meta">{{.AuthorName}} · <time datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span></div><p class="later-text">{{.Text}}</p>{{else}}<p class="later-unavailable">This message is no longer available.</p>{{end}}
<div class="later-actions">
{{if $.InProgressCurrent}}<form method="post" action="{{.CompleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Mark complete</button></form><form method="post" action="{{.ArchiveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Archive</button></form>{{else}}<form method="post" action="{{.RestoreURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Move to in progress</button></form>{{end}}
<form method="post" action="{{.RemoveURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="remove" type="submit">Remove from Later</button></form>
</div></li>{{else}}<li class="empty">No items in {{.StateTitle}}.</li>{{end}}</ul>
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more saved items</a></p>{{end}}
{{end}}
</main>{{end}}`

var laterTemplate = mustPage(laterMarkup)

// laterLiveScript reloads the Later views when their records change. It skips
// the reload while any <details> editor is open: a reload re-renders the
// editor closed, so a live event arriving mid-edit silently destroyed the
// person's unsaved changes. Saving navigates anyway, which refreshes the page.
const laterLiveScript = `<script>(function(){
if(!window.EventSource)return;
var cursor='';
try{cursor=sessionStorage.getItem('sameoldchat-last-event')||''}catch(error){cursor=''}
var stream=new EventSource('/events'+(cursor?'?last_event_id='+encodeURIComponent(cursor):''));
var timezone='UTC';try{timezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC'}catch(error){}
Array.prototype.forEach.call(document.querySelectorAll('[data-browser-timezone]'),function(input){input.value=timezone});
['saved_item.created','saved_item.changed','saved_item.removed','later_reminder.created','later_reminder.changed','later_reminder.completed','later_reminder.deleted','later_reminder.delivered','later_reminder.failed'].forEach(function(topic){
stream.addEventListener(topic,function(event){if(event.lastEventId){try{sessionStorage.setItem('sameoldchat-last-event',event.lastEventId)}catch(error){}}
if(document.querySelector('details[open]'))return;
window.location.reload()});
});
})();</script>`

const draftsAndSentMarkup = `{{define "title"}}Drafts &amp; sent · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);text-decoration:none;font-weight:700}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(900px,calc(100% - 32px));margin:28px auto 48px}.heading{margin-bottom:14px}.heading h2{margin:0 0 5px;font-size:25px}.heading p{margin:0;color:var(--muted)}
.tabs{display:flex;gap:4px;border-bottom:1px solid var(--line);margin-bottom:18px}.tabs a{padding:10px 14px;color:var(--muted);text-decoration:none;font-weight:800;border-bottom:3px solid transparent}.tabs a[aria-current="page"]{color:var(--text);border-color:var(--action)}
.work-list{display:grid;gap:9px;margin:0;padding:0;list-style:none}.work-item{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:12px;padding:15px;border:1px solid var(--line);border-radius:9px;background:var(--panel)}.work-meta{display:flex;align-items:center;gap:8px;flex-wrap:wrap;color:var(--muted);font-size:12px}.work-channel{font-weight:800;color:var(--text);text-decoration:none}.work-channel:hover{color:var(--action)}.work-text{margin:7px 0 0;white-space:pre-wrap;overflow-wrap:anywhere}.work-actions{display:flex;align-items:center;gap:7px;flex-wrap:wrap}.work-actions form{margin:0}.work-actions button,.work-actions summary{border:1px solid var(--field-line);border-radius:6px;background:var(--panel-strong);color:var(--text);padding:7px 10px;font-weight:800;cursor:pointer}.work-actions .danger{color:var(--danger)}.edit-panel{position:absolute;right:0;z-index:3;width:min(380px,calc(100vw - 32px));padding:14px;border:1px solid var(--line);border-radius:9px;background:var(--panel);box-shadow:0 12px 30px var(--shadow)}.edit-panel label{display:grid;gap:5px;margin-bottom:10px;font-weight:700}.edit-panel textarea,.edit-panel input{box-sizing:border-box;width:100%;padding:8px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.failed{color:var(--danger);font-weight:800}.empty{padding:28px;border:1px dashed var(--line);border-radius:9px;color:var(--muted);text-align:center}.pager{text-align:center;margin-top:18px}
@media(max-width:600px){.bar{padding:0 12px}.layout{width:min(100% - 20px,900px);margin-top:18px}.work-item{grid-template-columns:minmax(0,1fr)}.work-actions{align-items:stretch}.work-actions form,.work-actions button,.work-actions details{width:100%}.work-actions summary{box-sizing:border-box;width:100%;text-align:center}}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `<script>(function(){
document.querySelectorAll('[data-local-datetime]').forEach(function(input){
var date=new Date(input.getAttribute('data-local-datetime'));if(isNaN(date.getTime()))return;
var pad=function(n){return String(n).padStart(2,'0')};
input.value=date.getFullYear()+'-'+pad(date.getMonth()+1)+'-'+pad(date.getDate())+'T'+pad(date.getHours())+':'+pad(date.getMinutes());
});
document.querySelectorAll('.edit-panel').forEach(function(form){
var input=form.querySelector('[data-schedule-at]');var hidden=form.querySelector('input[name="post_at"]');
if(!input||!hidden)return;
var setPostAt=function(){
if(!input.value){hidden.value='';return}
var millis=new Date(input.value).getTime();hidden.value=isNaN(millis)?'':String(Math.floor(millis/1000));
};
input.addEventListener('input',setPostAt);input.addEventListener('change',setPostAt);
form.addEventListener('submit',setPostAt);setPostAt();
});
})();</script>{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to chat</a><h1>Drafts &amp; sent</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout">
<div class="heading"><h2>Drafts &amp; sent</h2><p>Continue drafts, manage scheduled messages, or return to something you sent.</p></div>
<nav class="tabs" aria-label="Drafts and sent views">
<a href="/app/drafts?channel={{.Channel}}&amp;tab=drafts" {{if eq .ActiveTab "drafts"}}aria-current="page"{{end}}>Drafts</a>
<a href="/app/drafts?channel={{.Channel}}&amp;tab=scheduled" {{if eq .ActiveTab "scheduled"}}aria-current="page"{{end}}>Scheduled</a>
<a href="/app/drafts?channel={{.Channel}}&amp;tab=sent" {{if eq .ActiveTab "sent"}}aria-current="page"{{end}}>Sent</a>
</nav>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if eq .ActiveTab "drafts"}}<ul class="work-list" aria-label="Drafts">{{range .Drafts}}<li class="work-item"><div><div class="work-meta">{{if .OpenURL}}<a class="work-channel" href="{{.OpenURL}}">{{.ChannelPrefix}}{{.ChannelName}}</a>{{else}}<span class="work-channel">{{.ChannelName}}</span>{{end}}<span>Updated <time datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span>{{if .AttachmentCount}}<span>{{.AttachmentCount}} attachment{{if ne .AttachmentCount 1}}s{{end}}</span>{{end}}</div><p class="work-text">{{if .Text}}{{.Text}}{{else}}Attachment draft{{end}}</p></div><div class="work-actions">{{if .OpenURL}}<a href="{{.OpenURL}}">Continue</a>{{end}}<form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger" type="submit" aria-label="Delete draft in {{.ChannelName}}">Delete</button></form></div></li>{{else}}<li class="empty">You have no drafts.</li>{{end}}</ul>{{end}}
{{if eq .ActiveTab "scheduled"}}<ul class="work-list" aria-label="Scheduled messages">{{range .Scheduled}}<li class="work-item scheduled-item"><div><div class="work-meta">{{if .ConversationURL}}<a class="work-channel" href="{{.ConversationURL}}">{{.ChannelPrefix}}{{.ChannelName}}</a>{{else}}<span class="work-channel">{{.ChannelName}}</span>{{end}}<span>{{.Status}} for <time datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span>{{if .AttachmentCount}}<span>{{.AttachmentCount}} attachment{{if ne .AttachmentCount 1}}s{{end}}</span>{{end}}{{if .Failure}}<span class="failed">{{.Failure}}</span>{{end}}</div><p class="work-text">{{.DisplayText}}</p></div><div class="work-actions"><details><summary>Edit</summary><form class="edit-panel" method="post" action="{{.UpdateURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><label>Message<textarea name="text" maxlength="40000" {{if not .AttachmentCount}}required{{end}}>{{.Text}}</textarea></label>{{if .AttachmentCount}}<p class="work-meta">Attached files stay with this scheduled message.</p>{{end}}<label>Send date and time<input type="datetime-local" name="schedule_at" data-schedule-at data-local-datetime="{{.MachineTime}}" required></label><input type="hidden" name="post_at"><button type="submit">Save changes</button></form></details><form method="post" action="{{.SendNowURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Send now</button></form><form method="post" action="{{.CancelURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button class="danger" type="submit" aria-label="Cancel scheduled message in {{.ChannelName}}">Cancel message</button></form></div></li>{{else}}<li class="empty">You have no scheduled messages.</li>{{end}}</ul>{{end}}
{{if eq .ActiveTab "sent"}}<ul class="work-list" aria-label="Sent messages">{{range .Sent}}<li class="work-item"><div><div class="work-meta">{{if .OpenURL}}<a class="work-channel" href="{{.OpenURL}}">{{.ChannelPrefix}}{{.ChannelName}}</a>{{else}}<span class="work-channel">{{.ChannelName}}</span>{{end}}<span>Sent <time datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span></div><p class="work-text">{{.Text}}</p></div>{{if .OpenURL}}<div class="work-actions"><a href="{{.OpenURL}}">View conversation</a></div>{{end}}</li>{{else}}<li class="empty">You have no recently sent messages.</li>{{end}}</ul>{{end}}
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">{{.MoreLabel}}</a></p>{{end}}
</main>{{end}}`

var draftsAndSentTemplate = mustPage(draftsAndSentMarkup)

const identityMarkup = `{{define "title"}}{{.Heading}} · SameOldChat{{end}}
{{define "styles"}}<style>
body{min-height:100vh}
.bar{display:flex;align-items:center;gap:16px;padding:14px 22px;background:var(--panel);border-bottom:1px solid var(--line)}
.bar a{color:var(--action);font-weight:700;text-decoration:none}
.bar .theme-toggle{margin-left:auto;border-color:var(--line);color:var(--text)}
.bar .theme-toggle:hover{background:var(--hover)}
.layout{width:min(760px,calc(100% - 32px));margin:40px auto}
.card{padding:28px;background:var(--panel);border:1px solid var(--line);border-radius:16px;box-shadow:var(--shadow)}
.identity-heading{display:grid;grid-template-columns:72px minmax(0,1fr);align-items:center;gap:18px;margin-bottom:8px}
.avatar{width:72px;height:72px;border-radius:16px;display:grid;place-items:center;background:linear-gradient(135deg,var(--accent),#2f7f9c);color:#fff;font-size:1.8rem;font-weight:800;object-fit:cover;overflow:hidden;text-transform:uppercase}
h1{margin:0;font-size:clamp(1.8rem,5vw,2.8rem);line-height:1.1}
.lede{margin:0 0 24px;color:var(--muted)}
dl{display:grid;grid-template-columns:minmax(120px,180px) minmax(0,1fr);margin:0 0 24px;border-top:1px solid var(--line)}
dt,dd{margin:0;padding:12px 0;border-bottom:1px solid var(--line)}
dt{color:var(--muted);font-weight:700}
dd{overflow-wrap:anywhere}
.button{border:0;border-radius:8px;padding:11px 17px;background:var(--danger);color:var(--on-strong);font-weight:800}
@media(max-width:540px){.layout{margin:22px auto}.card{padding:20px}.identity-heading{grid-template-columns:56px minmax(0,1fr)}.avatar{width:56px;height:56px;border-radius:12px}dl{grid-template-columns:minmax(0,1fr)}dt{padding-bottom:0;border-bottom:0}dd{padding-top:3px}}
</style>{{end}}
{{define "content"}}<header class="bar"><strong>SameOldChat</strong><a href="/app">Back to chat</a><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false"><span aria-hidden="true">☾</span><span class="visually-hidden">Dark theme</span></button></header><main class="layout"><section class="card" aria-labelledby="identity-heading"><div class="identity-heading">{{if .AvatarURL}}<img class="avatar" src="{{.AvatarURL}}" alt="Avatar for {{.Username}}">{{else}}<span class="avatar" role="img" aria-label="Avatar for {{.Username}}">{{.Avatar}}</span>{{end}}<h1 id="identity-heading">{{.Heading}}</h1></div><p class="lede">Your verified Shauth identity and this immutable application release.</p><dl><dt>Username</dt><dd data-testid="validation-username">{{.Username}}</dd><dt>Email</dt><dd data-testid="validation-email">{{.Email}}</dd><dt>Role</dt><dd data-testid="validation-role">{{.Role}}</dd><dt>Release</dt><dd data-testid="validation-release">{{.Release}}</dd></dl><form method="post" action="/logout"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button class="button" type="submit">Sign out</button></form></section></main>{{end}}`

var identityTemplate = mustPage(identityMarkup)

const errorMarkup = `{{define "title"}}{{.Heading}} · SameOldChat{{end}}
{{define "styles"}}<style>
.layout{width:min(640px,calc(100% - 32px));margin:64px auto;padding:26px;background:var(--panel);border:1px solid var(--line);border-radius:12px}
h1{margin:0 0 10px;font-size:24px}
p{margin:0 0 18px;color:var(--muted)}
a{font-weight:700}
</style>{{end}}
{{define "content"}}<main class="layout"><h1>{{.Heading}}</h1><p>{{.Message}}</p><a href="/app">Back to chat</a></main>{{end}}`

var errorTemplate = mustPage(errorMarkup)

const documentsMarkup = `{{define "title"}}{{.Title}} · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(940px,calc(100% - 32px));margin:28px auto 56px}.heading{display:flex;align-items:start;justify-content:space-between;gap:20px;margin-bottom:20px}.heading h2,.heading p{margin:0}.heading p{color:var(--muted);margin-top:5px}
.create{padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--panel);margin-bottom:18px}.create summary{cursor:pointer;font-weight:800}.create form{display:grid;gap:10px;margin-top:14px}.create label{display:grid;gap:6px;font-weight:700}.create input,.create textarea{padding:9px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.create textarea{min-height:130px;resize:vertical}.create button{justify-self:start;border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}
.grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:12px}.card{display:grid;gap:10px;min-height:150px;padding:17px;border:1px solid var(--line);border-radius:10px;background:var(--panel);color:var(--text);text-decoration:none}.card:hover{border-color:var(--action);background:var(--hover)}.card h3,.card p{margin:0}.card p{color:var(--muted);white-space:pre-wrap;overflow-wrap:anywhere}.card time{align-self:end;color:var(--muted);font-size:12px}.empty{padding:32px;border:1px dashed var(--line);border-radius:10px;text-align:center;color:var(--muted)}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app">← Back to chat</a><h1>{{.Title}}</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">
<div class="heading"><div><h2>{{.Title}}</h2><p>{{if eq .Kind "canvas"}}Create, read, and revise shared workspace documents.{{else}}Track work in persisted, structured rows.{{end}}</p></div></div>
{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if .CanWrite}}<details class="create"><summary>Create {{if eq .Kind "canvas"}}a canvas{{else}}a list{{end}}</summary><form method="post" action="/app/{{if eq .Kind "canvas"}}canvases{{else}}lists{{end}}/create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label>Name<input name="title" maxlength="255" required></label>{{if eq .Kind "canvas"}}<label>Content<textarea name="body" maxlength="100000" placeholder="Start writing…"></textarea></label>{{else}}<label><span><input type="checkbox" name="todo_mode" value="true"> Use as a to-do list</span></label>{{end}}<button type="submit">Create</button></form></details>{{end}}
<div class="grid">{{if eq .Kind "canvas"}}{{range .Canvases}}<a class="card" href="{{.URL}}"><h3>{{.Title}}</h3><p>{{.Preview}}</p><time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></a>{{else}}<p class="empty">No canvases yet.</p>{{end}}{{else}}{{range .Lists}}<a class="card" href="{{.URL}}"><h3>{{.Title}}</h3><p>{{.Preview}}</p><time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></a>{{else}}<p class="empty">No lists yet.</p>{{end}}{{end}}</div>
{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more</a></p>{{end}}</main>{{end}}`

var documentsTemplate = mustPage(documentsMarkup)

// sharingStyle dresses the shared sharing section on every page that shows it.
const sharingStyle = `.sharing{margin-top:22px;padding-top:16px;border-top:1px solid var(--line)}.sharing h3{margin:0 0 10px}.grants{list-style:none;margin:0 0 14px;padding:0;display:grid;gap:8px}.grant{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding:8px 10px;border:1px solid var(--line);border-radius:8px}.grant-who{font-weight:700}.grant-access,.grant-reason{color:var(--muted);font-size:12px}.grant form{margin-left:auto}.grant button{border:1px solid var(--line);border-radius:7px;padding:6px 10px;background:transparent;color:var(--text);font-weight:700}.share{display:grid;gap:8px;max-width:420px}.share select{padding:9px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text)}.share button{justify-self:start;border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}`

// sharingSection is the sharing surface, written once because a canvas and a
// list carry the same grant model and a member deciding who may open one is
// answering the same question either way. SharePath is the document's own path,
// so the forms post beside it rather than to a route this markup has to know.
const sharingSection = `<section class="sharing" aria-labelledby="sharing-heading"><h3 id="sharing-heading">Sharing</h3>
<ul class="grants">{{range .Grants}}<li class="grant"><span class="grant-who">{{.Name}}</span><span class="grant-access">{{.Access}}</span>{{if .Target}}{{if $.CanShare}}<form method="post" action="{{$.SharePath}}/share/revoke"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="target" value="{{.Target}}"><button type="submit">Stop sharing with {{.Name}}</button></form>{{end}}{{else if .Reason}}<span class="grant-reason">{{.Reason}}</span>{{end}}</li>{{end}}</ul>
{{if .CanShare}}<form class="share" method="post" action="{{.SharePath}}/share"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label for="share-target">Share with</label><select id="share-target" name="target" required><option value="">Choose a member or channel</option>{{range .ShareTargets}}<option value="{{.Value}}">{{.Kind}}: {{.Name}}</option>{{end}}</select>
<label for="share-access">Access</label><select id="share-access" name="access"><option value="read">Can view</option><option value="write">Can edit</option></select>
<button type="submit">Share {{.ShareNoun}}</button></form>
<p class="read-only">Only you can change who this {{.ShareNoun}} is shared with. Sharing with a channel shares it with everyone in that channel.</p>
{{else}}<p class="read-only">Only the owner can change who this {{.ShareNoun}} is shared with.</p>{{end}}
</section>`

const canvasMarkup = `{{define "title"}}{{.Title}} · Canvas · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(860px,calc(100% - 32px));margin:28px auto 56px}.canvas{padding:28px;border:1px solid var(--line);border-radius:12px;background:var(--panel)}.canvas h2{margin:0 0 8px}.canvas .meta{color:var(--muted);font-size:12px}.canvas-body{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.6}
.canvas-section{margin-top:20px;padding-top:16px;border-top:1px solid var(--line)}.canvas-section:first-of-type{border-top:0;padding-top:0}
` + sharingStyle + `
.editor{display:grid;gap:11px;margin-top:16px;padding-top:16px;border-top:1px dashed var(--line)}.editor label{display:grid;gap:6px;font-weight:700}.editor input,.editor textarea{padding:10px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text)}.editor textarea{min-height:300px;resize:vertical}.actions{display:flex;gap:10px;flex-wrap:wrap}.actions button{border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}.delete{margin-top:18px}.delete button{border:1px solid var(--danger);border-radius:7px;padding:8px 12px;background:transparent;color:var(--danger);font-weight:800}
</style>{{end}}
{{define "scripts"}}` + localTimeScript + `{{end}}
{{define "content"}}<header class="bar"><a href="/app/canvases">← Canvases</a><h1>Canvas</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}<article class="canvas"><h2>{{.Title}}</h2><p class="meta">Updated <time datetime="{{.UpdatedAt}}">{{.UpdatedAt}}</time></p>{{if .ReadOnlyReason}}<p class="notice" role="note">{{.ReadOnlyReason}}</p>{{end}}
{{range .Sections}}<section class="canvas-section" aria-label="Canvas part {{.Position}}{{if .Type}}, {{.Type}}{{end}}">
  <div class="canvas-body">{{.Text}}</div>
  {{if and $.CanWrite .Editable}}<form class="editor" method="post" action="/app/canvases/{{$.ID}}/update">
    <input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="section_id" value="{{.ID}}">
    <label>Title<input name="title" maxlength="255" value="{{$.Title}}" required></label>
    <label>Section {{.Position}} content<textarea name="body" maxlength="100000">{{.Text}}</textarea></label>
    <div class="actions"><button type="submit">Save section {{.Position}}</button></div>
  </form>{{else if $.CanWrite}}<p class="notice" role="note">This section is {{.Type}} content. It is shown as stored; editing it here would flatten it, so this client does not offer that.</p>{{end}}
</section>{{end}}
{{if and .CanWrite (not .Sections)}}<form class="editor" method="post" action="/app/canvases/{{.ID}}/update"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="section_id" value=""><label>Title<input name="title" maxlength="255" value="{{.Title}}" required></label><label>Content<textarea name="body" maxlength="100000"></textarea></label><div class="actions"><button type="submit">Save changes</button></div></form>{{end}}<section class="canvas-comments" aria-labelledby="canvas-comments-heading"><h3 id="canvas-comments-heading">Comments</h3>
{{if .Comments}}<ul class="comments">{{range .Comments}}<li class="comment"><span class="comment-head"><span class="comment-author">{{.AuthorName}}</span>{{if .SectionName}}<span class="comment-anchor">on {{.SectionName}}</span>{{end}}<time class="comment-time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time></span><p class="comment-text">{{.Text}}</p>{{if .DeleteURL}}<form method="post" action="{{.DeleteURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><button type="submit">Delete comment</button></form>{{end}}</li>{{end}}</ul>{{else}}<p class="empty">No comments yet.</p>{{end}}
<form class="new-comment" method="post" action="/app/canvases/{{.ID}}/comments"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label for="comment-section">About</label><select id="comment-section" name="section_id"><option value="">The whole canvas</option>{{range .Sections}}<option value="{{.ID}}">Section {{.Position}}</option>{{end}}</select>
<label for="comment-text">Comment</label><textarea id="comment-text" name="text" maxlength="4000" rows="2" required></textarea>
<button type="submit">Add comment</button></form>
<p class="read-only">Anyone who can read this canvas can comment on it. A comment belongs to whoever wrote it, and only they can delete it.</p>
</section>
` + sharingSection + `
{{if .Revisions}}<section class="canvas-history" aria-labelledby="canvas-history-heading"><h3 id="canvas-history-heading">History</h3>
<ul class="revisions">{{range .Revisions}}<li class="revision"><span class="revision-head"><span class="revision-title">{{.Title}}</span><time class="revision-time" datetime="{{.MachineTime}}">{{.DisplayTime}}</time>{{if .EditorName}}<span class="revision-editor">replaced by {{.EditorName}}</span>{{end}}</span>{{if .Excerpt}}<p class="revision-excerpt">{{.Excerpt}}</p>{{end}}{{if .RestoreURL}}<form method="post" action="{{.RestoreURL}}"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="version" value="{{.Version}}"><button type="submit">Restore this revision</button></form>{{end}}</li>{{end}}</ul>
<p class="read-only">Restoring is an ordinary edit: the current content becomes a revision of its own, so restoring the wrong one can be undone.</p>
</section>{{end}}
{{if .CanDelete}}<form class="delete" method="post" action="/app/canvases/{{.ID}}/delete"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><button type="submit">Delete canvas</button></form>{{end}}</article></main>{{end}}`

var canvasTemplate = mustPage(canvasMarkup)

const channelCanvasMarkup = `{{define "title"}}Canvas · #{{.ChannelName}} · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(720px,calc(100% - 32px));margin:28px auto 56px;padding:26px;border:1px solid var(--line);border-radius:12px;background:var(--panel)}.layout h2{margin:0 0 10px}.layout p{color:var(--muted)}
form{display:grid;gap:10px;max-width:420px;margin-top:18px}label{display:grid;gap:6px;font-weight:700}input{padding:9px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text)}button{justify-self:start;border:0;border-radius:7px;padding:9px 14px;background:var(--action);color:var(--on-strong);font-weight:800}
</style>{{end}}
{{define "content"}}<header class="bar"><a href="/app?channel={{.Channel}}">← Back to #{{.ChannelName}}</a><h1>Canvas</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
<h2>#{{.ChannelName}} has no canvas yet</h2>
<p>A conversation's canvas is a document that belongs to the conversation itself, and everyone in it can read and write it. It is not the same as a canvas shared into the channel: a conversation has exactly one of these.</p>
{{if .CanCreate}}<form method="post" action="/app/channel-canvas/create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="channel" value="{{.Channel}}">
<label for="canvas-title">Title<input id="canvas-title" name="title" maxlength="255" value="{{.ChannelName}}" required></label>
<button type="submit">Create the canvas</button></form>
{{else}}<p class="read-only">Creating one needs permission to write canvases, which this session does not have.</p>{{end}}</main>{{end}}`

var channelCanvasTemplate = mustPage(channelCanvasMarkup)

const listMarkup = `{{define "title"}}{{.Name}} · List · SameOldChat{{end}}
{{define "styles"}}<style>
.bar{height:52px;background:var(--accent);color:var(--on-accent);display:flex;align-items:center;padding:0 20px;gap:16px}.bar a{color:var(--on-accent);font-weight:700;text-decoration:none}.bar h1{margin:0 auto 0 0;font-size:18px}
.layout{width:min(940px,calc(100% - 28px));margin:26px auto 56px}.heading{display:flex;align-items:center;justify-content:space-between;gap:16px}.heading h2{margin:0}.new-item{display:flex;gap:8px;margin:18px 0}.new-item input{flex:1;min-width:0;padding:9px;border:1px solid var(--field-line);border-radius:7px;background:var(--field);color:var(--text)}button{border:0;border-radius:7px;padding:9px 13px;background:var(--action);color:var(--on-strong);font-weight:800}
.items{list-style:none;margin:0;padding:0;border:1px solid var(--line);border-radius:10px;overflow:hidden}.item{display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:11px;padding:12px 14px;background:var(--panel);border-bottom:1px solid var(--line)}.item:last-child{border-bottom:0}.item.archived .title{text-decoration:line-through;color:var(--muted)}.item form{margin:0}.item-open{color:var(--action);font-weight:700;text-decoration:none;font-size:13px;white-space:nowrap}.list-table .item-open{font-size:13px}.item button{background:var(--panel-strong);color:var(--text);border:1px solid var(--field-line)}.empty{padding:30px;text-align:center;color:var(--muted)}.mode{color:var(--muted);font-size:13px}
.remove-column{margin:10px 0 16px}.remove-column summary{cursor:pointer;font-weight:800}.column-list{list-style:none;margin:10px 0 0;padding:0;display:grid;gap:8px}.column-row{display:flex;align-items:center;gap:10px;flex-wrap:wrap;padding:8px 10px;border:1px solid var(--line);border-radius:8px}.column-name{font-weight:700}.column-type,.column-reason{color:var(--muted);font-size:12px}.column-row form{margin-left:auto}.column-row button{border:1px solid var(--line);border-radius:7px;padding:6px 10px;background:transparent;color:var(--text);font-weight:700}
.list-views{display:flex;gap:6px;margin:14px 0 4px}.list-views a{padding:6px 12px;border:1px solid var(--line);border-radius:7px;text-decoration:none;color:var(--text);font-weight:700}.list-views a.current{background:var(--action);color:var(--on-strong);border-color:var(--action)}
.list-groups{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:8px 0 4px;font-size:13px}.list-groups .group-label{color:var(--muted);font-weight:700}.list-groups a{padding:4px 9px;border:1px solid var(--line);border-radius:6px;text-decoration:none;color:var(--text)}.list-groups a.current{background:var(--panel-strong);font-weight:800}
.list-filter{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:8px 0}.list-filter label{font-weight:700;font-size:13px;color:var(--muted)}.list-filter select{padding:7px;border:1px solid var(--field-line);border-radius:6px;background:var(--field);color:var(--text)}.list-filter .clear-filter{color:var(--action);font-size:13px;font-weight:700}
.board{display:grid;grid-template-columns:repeat(auto-fill,minmax(240px,1fr));gap:14px;margin-top:12px}.lane{background:var(--panel-strong);border:1px solid var(--line);border-radius:10px;padding:10px}.lane-head{margin:2px 4px 10px;font-size:14px;display:flex;align-items:center;gap:8px}.lane-count{color:var(--muted);font-weight:700;font-size:12px}.lane-items{border:0;border-radius:0;background:transparent;overflow:visible}.lane-items .item{display:flex;flex-direction:column;align-items:flex-start;gap:6px;border:1px solid var(--line);border-radius:8px;margin-bottom:8px}.lane-items .item:last-child{margin-bottom:0}.lane-items .empty{padding:12px}
.table-wrap{overflow-x:auto;margin-top:12px}.list-table{width:100%;border-collapse:collapse;border:1px solid var(--line);border-radius:10px}.list-table th,.list-table td{text-align:left;padding:9px 11px;border-bottom:1px solid var(--line);vertical-align:top}.list-table thead th{background:var(--panel-strong);font-size:13px;white-space:nowrap}.list-table thead th a{color:var(--text);text-decoration:none;font-weight:800}.list-table tbody tr:last-child td{border-bottom:0}.list-table tr.archived td{color:var(--muted)}.list-table td.overdue{color:var(--danger);font-weight:700}.list-table .row-actions{display:flex;flex-wrap:wrap;gap:6px}.list-table .row-actions form{margin:0}.list-table .row-actions button{background:var(--panel-strong);color:var(--text);border:1px solid var(--field-line)}.list-table td.empty{text-align:center;color:var(--muted);padding:24px}
.calendar-nav{display:flex;align-items:center;gap:14px;margin:10px 0}.calendar-nav a{color:var(--action);font-weight:700;text-decoration:none}.calendar-month{font-weight:800}
.calendar{width:100%;border-collapse:collapse;table-layout:fixed;border:1px solid var(--line)}.calendar th{background:var(--panel-strong);padding:6px;font-size:12px;border:1px solid var(--line)}.calendar td.cal-day{border:1px solid var(--line);vertical-align:top;height:88px;padding:4px;background:var(--panel)}.calendar td.cal-out{background:var(--field);color:var(--muted)}.calendar td.cal-today{outline:2px solid var(--focus);outline-offset:-2px}.cal-num{display:block;font-size:12px;font-weight:700;color:var(--muted);margin-bottom:3px}.cal-item{display:block;font-size:12px;padding:2px 5px;margin-bottom:3px;border-radius:5px;background:var(--action);color:var(--on-strong);text-decoration:none;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.cal-item.cal-done{background:var(--panel-strong);color:var(--muted);text-decoration:line-through}
` + sharingStyle + `
</style>{{end}}
{{define "listRow"}}{{$row := .}}<li class="item{{if .Archived}} archived{{end}}"><span aria-hidden="true">{{if .Archived}}✓{{else}}○{{end}}</span>{{if .Cells}}<span class="cells">{{range .Cells}}<span class="cell"><span class="cell-name">{{.ColumnName}}</span><span class="cell-value">{{if .Value}}{{.Value}}{{else}}—{{end}}</span></span>{{end}}</span>{{else}}<span class="title">{{.Title}}</span>{{end}}{{if .AssigneeName}}<span class="item-assignee">{{.AssigneeName}}</span>{{end}}{{if .DueDate}}<span class="item-due{{if .Overdue}} overdue{{end}}">Due {{.DueDate}}{{if .Overdue}} · overdue{{end}}</span>{{end}}<a class="item-open" href="/app/lists/{{.ListID}}/items/{{.ID}}">Open</a>{{if .CanWrite}}<form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/toggle"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="archived" value="{{if .Archived}}false{{else}}true{{end}}"><button type="submit">{{if .Archived}}Restore{{else}}Complete{{end}}</button></form>
<details class="item-assign"><summary>{{if .AssigneeName}}Reassign{{else}}Assign{{end}}</summary><form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/assign"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label for="assignee-{{.ID}}">Assign to</label><select id="assignee-{{.ID}}" name="assignee"><option value="">Nobody</option>{{range $row.Members}}<option value="{{.ID}}"{{if eq .ID $row.AssigneeID}} selected{{end}}>{{.Name}}</option>{{end}}</select>
<label for="due-{{.ID}}">Due</label><input id="due-{{.ID}}" type="date" name="due" value="{{.DueDate}}">
<button type="submit">Save assignment</button></form></details>
<details class="item-delete"><summary>Delete</summary><form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/delete"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<p class="read-only">Deleting removes this item and everything on it for good. Completing it instead keeps it and can be undone.</p>
<button type="submit">Delete this item</button></form></details>{{end}}</li>{{end}}
{{define "content"}}<header class="bar"><a href="/app/lists">← Lists</a><h1>List</h1><button class="theme-toggle" id="theme-toggle" type="button" aria-pressed="false">Theme</button></header><main class="layout">{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}<div class="heading"><div><h2>{{.Name}}</h2><span class="mode">{{if .TodoMode}}To-do list{{else}}List{{end}}</span></div></div><nav class="list-views" aria-label="List view"><a href="{{.ListViewURL}}"{{if and (not .BoardActive) (not .TableActive)}} class="current" aria-current="page"{{end}}>List</a>{{if .TableViewURL}}<a href="{{.TableViewURL}}"{{if .TableActive}} class="current" aria-current="page"{{end}}>Table</a>{{end}}{{if .BoardViewURL}}<a href="{{.BoardViewURL}}"{{if .BoardActive}} class="current" aria-current="page"{{end}}>Board</a>{{end}}{{if .CalendarViewURL}}<a href="{{.CalendarViewURL}}"{{if .CalendarActive}} class="current" aria-current="page"{{end}}>Calendar</a>{{end}}</nav>{{if .BoardActive}}<nav class="list-groups" aria-label="Group by"><span class="group-label">Group by</span>{{range .GroupChoices}}<a href="{{.URL}}"{{if .Selected}} class="current" aria-current="true"{{end}}>{{.Name}}</a>{{end}}</nav>{{end}}{{if .FilterOptions}}<form class="list-filter" method="get" action="/app/lists/{{.ID}}" aria-label="Filter items"><input type="hidden" name="view" value="{{.View}}">{{if .BoardActive}}<input type="hidden" name="group" value="{{.GroupKey}}">{{end}}{{if .TableActive}}<input type="hidden" name="sort" value="{{.SortKey}}"><input type="hidden" name="dir" value="{{.SortDir}}">{{end}}<label for="list-filter-select">Filter</label><select id="list-filter-select" name="filter"><option value="">All items</option>{{range .FilterOptions}}<option value="{{.Value}}"{{if .Selected}} selected{{end}}>{{.Label}}</option>{{end}}</select><button type="submit">Apply</button>{{if .FilterActive}}<a class="clear-filter" href="{{.ClearFilterURL}}">Clear filter</a>{{end}}</form>{{end}}{{if .CanWrite}}<form class="new-item" method="post" action="/app/lists/{{.ID}}/items/create"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label class="visually-hidden" for="new-list-item">New item</label><input id="new-list-item" name="title" maxlength="1000" placeholder="Add an item" required><button type="submit">Add</button></form>{{end}}{{if .Columns}}<p class="list-columns muted">Columns: {{range $index, $column := .Columns}}{{if $index}}, {{end}}{{$column.Name}} ({{$column.Type}}){{end}}</p>
{{if .CanWrite}}<details class="remove-column"><summary>Remove a column</summary>
<p class="read-only">Removing a column deletes what every item recorded under it, for good. The first column names the item and stays.</p>
<ul class="column-list">{{range .Columns}}<li class="column-row"><span class="column-name">{{.Name}}</span><span class="column-type">{{.Type}}</span>{{if .Primary}}<span class="column-reason">names the item</span>{{else}}<form method="post" action="/app/lists/{{$.ID}}/columns/remove"><input type="hidden" name="_csrf" value="{{$.CSRFToken}}"><input type="hidden" name="key" value="{{.Key}}"><button type="submit">Remove {{.Name}}</button></form>{{end}}</li>{{end}}</ul>
</details>{{end}}{{end}}
{{if .CanWrite}}<details class="add-column"><summary>Add a column</summary><form method="post" action="/app/lists/{{.ID}}/columns"><input type="hidden" name="_csrf" value="{{.CSRFToken}}">
<label for="column-name">Name</label><input id="column-name" name="name" maxlength="80" required>
<label for="column-type">Type</label><select id="column-type" name="type"><option value="text">Text</option><option value="number">Number</option><option value="date">Date</option><option value="select">Select</option><option value="checkbox">Checkbox</option><option value="person">Person</option></select>
<label for="column-options">Options</label><input id="column-options" name="options" maxlength="500" placeholder="open, done" aria-describedby="column-options-hint">
<span id="column-options-hint" class="muted">Comma separated, and required for a Select column.</span>
<button type="submit">Add column</button></form>
<p class="read-only">A column is added to the end. Removing one is not offered here: it would have to remove that value from every item, which is a deletion worth asking for deliberately.</p>
</details>{{end}}
{{if .BoardActive}}{{if .BoardTruncated}}<p class="notice" role="status">This list has more than 1,000 items; the board groups the first 1,000.</p>{{end}}<div class="board">{{range .Lanes}}<section class="lane" aria-label="{{.Label}} ({{.Count}} item{{if ne .Count 1}}s{{end}})"><h3 class="lane-head">{{.Label}} <span class="lane-count">{{.Count}}</span></h3><ul class="items lane-items">{{range .Items}}{{template "listRow" .}}{{else}}<li class="empty">None</li>{{end}}</ul></section>{{end}}</div>{{else if .TableActive}}{{if .BoardTruncated}}<p class="notice" role="status">This list has more than 1,000 items; the table shows the first 1,000.</p>{{end}}<div class="table-wrap"><table class="list-table"><thead><tr>{{range .TableHeaders}}<th scope="col" aria-sort="{{if eq .Sorted "asc"}}ascending{{else if eq .Sorted "desc"}}descending{{else}}none{{end}}"><a href="{{.URL}}">{{.Name}}{{if eq .Sorted "asc"}} <span aria-hidden="true">▲</span>{{else if eq .Sorted "desc"}} <span aria-hidden="true">▼</span>{{end}}</a></th>{{end}}<th scope="col">Assignee</th><th scope="col">Due</th><th scope="col">Open</th>{{if .CanWrite}}<th scope="col">Actions</th>{{end}}</tr></thead><tbody>{{range .Items}}{{$row := .}}<tr{{if .Archived}} class="archived"{{end}}>{{range .Cells}}<td>{{if .Value}}{{.Value}}{{else}}—{{end}}</td>{{end}}<td>{{if .AssigneeName}}{{.AssigneeName}}{{else}}—{{end}}</td><td{{if .Overdue}} class="overdue"{{end}}>{{if .DueDate}}{{.DueDate}}{{else}}—{{end}}</td><td><a class="item-open" href="/app/lists/{{.ListID}}/items/{{.ID}}">Open</a></td>{{if $.CanWrite}}<td><div class="row-actions"><form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/toggle"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><input type="hidden" name="archived" value="{{if .Archived}}false{{else}}true{{end}}"><button type="submit">{{if .Archived}}Restore{{else}}Complete{{end}}</button></form><details class="item-assign"><summary>{{if .AssigneeName}}Reassign{{else}}Assign{{end}}</summary><form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/assign"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><label for="tassignee-{{.ID}}">Assign to</label><select id="tassignee-{{.ID}}" name="assignee"><option value="">Nobody</option>{{range $row.Members}}<option value="{{.ID}}"{{if eq .ID $row.AssigneeID}} selected{{end}}>{{.Name}}</option>{{end}}</select><label for="tdue-{{.ID}}">Due</label><input id="tdue-{{.ID}}" type="date" name="due" value="{{.DueDate}}"><button type="submit">Save</button></form></details><details class="item-delete"><summary>Delete</summary><form method="post" action="/app/lists/{{.ListID}}/items/{{.ID}}/delete"><input type="hidden" name="_csrf" value="{{.CSRFToken}}"><p class="read-only">Deleting removes this item for good.</p><button type="submit">Delete</button></form></details></div></td>{{end}}</tr>{{else}}<tr><td class="empty" colspan="99">No items yet.</td></tr>{{end}}</tbody></table></div>{{else if .CalendarActive}}<nav class="calendar-nav" aria-label="Month"><a href="{{.PrevMonthURL}}">← Previous</a><span class="calendar-month">{{.MonthLabel}}</span><a href="{{.NextMonthURL}}">Next →</a></nav>{{if .DateChoices}}<nav class="list-groups" aria-label="Date column"><span class="group-label">By date</span>{{range .DateChoices}}<a href="{{.URL}}"{{if .Selected}} class="current" aria-current="true"{{end}}>{{.Name}}</a>{{end}}</nav>{{end}}{{if .BoardTruncated}}<p class="notice" role="status">This list has more than 1,000 items; the calendar shows the first 1,000.</p>{{end}}<div class="table-wrap"><table class="calendar" aria-label="{{.MonthLabel}} by {{.DateColumnName}}"><thead><tr><th scope="col">Sun</th><th scope="col">Mon</th><th scope="col">Tue</th><th scope="col">Wed</th><th scope="col">Thu</th><th scope="col">Fri</th><th scope="col">Sat</th></tr></thead><tbody>{{range .CalendarWeeks}}<tr>{{range .}}<td class="cal-day{{if not .InMonth}} cal-out{{end}}{{if .Today}} cal-today{{end}}"><span class="cal-num">{{.Day}}</span>{{range .Items}}<a class="cal-item{{if .Archived}} cal-done{{end}}" href="/app/lists/{{.ListID}}/items/{{.ID}}">{{.Title}}</a>{{end}}</td>{{end}}</tr>{{end}}</tbody></table></div>{{else}}<ul class="items">{{range .Items}}{{template "listRow" .}}{{else}}<li class="empty">No items yet.</li>{{end}}</ul>{{if .MoreURL}}<p class="pager"><a href="{{.MoreURL}}">Show more items</a></p>{{end}}{{end}}
` + sharingSection + `</main>{{end}}`

var listTemplate = mustPage(listMarkup)

// localTimeScript renders machine timestamps in the reader's own locale and
// zone. The server keeps the machine value in datetime= so the page is still
// readable without JavaScript.
const localTimeScript = `<script>(function(){window.sameoldchatLocalTimes=function(root){if(!root||!window.Intl)return;var nodes=root.querySelectorAll('time[datetime]');for(var index=0;index<nodes.length;index++){var value=new Date(nodes[index].getAttribute('datetime'));if(isNaN(value.getTime()))continue;nodes[index].textContent=value.toLocaleString(undefined,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'})}};window.sameoldchatLocalTimes(document)})();</script>`

// searchSuggestionsScript progressively enhances both workspace search inputs
// with the same accessible listbox. The anchors remain real destinations, and
// the form remains a complete non-JavaScript fallback.
const searchSuggestionsScript = `<script>(function(){
function bind(input){
if(input.getAttribute('data-search-suggestions-bound')==='true'||!input.form)return;
input.setAttribute('data-search-suggestions-bound','true');
var form=input.form;
var list=document.createElement('div');
list.className='search-suggestions';
list.id=input.id+'-suggestions';
list.setAttribute('role','listbox');
list.setAttribute('aria-label','Search suggestions');
list.hidden=true;
form.appendChild(list);
input.setAttribute('autocomplete','off');
input.setAttribute('aria-autocomplete','list');
input.setAttribute('aria-controls',list.id);
input.setAttribute('aria-expanded','false');
var items=[];
var active=-1;
var timer=0;
var request=null;
function close(){
active=-1;
list.hidden=true;
input.setAttribute('aria-expanded','false');
input.removeAttribute('aria-activedescendant');
}
function activate(index){
if(!items.length){close();return}
active=(index+items.length)%items.length;
for(var itemIndex=0;itemIndex<items.length;itemIndex++)items[itemIndex].setAttribute('aria-selected',itemIndex===active?'true':'false');
input.setAttribute('aria-activedescendant',items[active].id);
items[active].scrollIntoView({block:'nearest'});
}
function render(payload){
list.replaceChildren();
items=[];
active=-1;
var values=payload&&Array.isArray(payload.items)?payload.items:[];
for(var index=0;index<values.length;index++){
var value=values[index];
if(!value||typeof value.url!=='string'||value.url.charAt(0)!=='/'||value.url.charAt(1)==='/'||typeof value.label!=='string')continue;
var link=document.createElement('a');
link.className='search-suggestion';
link.id=list.id+'-'+items.length;
link.href=value.url;
link.setAttribute('role','option');
link.setAttribute('aria-selected','false');
var label=document.createElement('span');
label.className='search-suggestion-label';
label.textContent=value.label;
var kind=document.createElement('span');
kind.className='search-suggestion-kind';
kind.textContent=value.description||value.kind||'Result';
link.appendChild(label);
link.appendChild(kind);
link.addEventListener('pointermove',function(event){activate(items.indexOf(event.currentTarget))});
list.appendChild(link);
items.push(link);
}
if(items.length){
list.hidden=false;
input.setAttribute('aria-expanded','true');
}else close();
}
function load(){
if(request)request.abort();
request=new AbortController();
var parameters=new URLSearchParams();
parameters.set('q',input.value);
var channel=form.querySelector('input[name=channel]');
if(channel&&channel.value)parameters.set('channel',channel.value);
fetch('/app/search/suggestions?'+parameters.toString(),{headers:{Accept:'application/json'},credentials:'same-origin',signal:request.signal}).then(function(response){
if(!response.ok)throw new Error('suggestions unavailable');
return response.json();
}).then(render).catch(function(error){if(error.name!=='AbortError')close()});
}
input.addEventListener('focus',load);
input.addEventListener('input',function(){
clearTimeout(timer);
if(request)request.abort();
items=[];
list.replaceChildren();
close();
timer=window.setTimeout(load,120);
});
input.addEventListener('keydown',function(event){
if(event.key==='ArrowDown'||event.key==='ArrowUp'){
if(!items.length){load();return}
event.preventDefault();
activate(active+(event.key==='ArrowDown'?1:-1));
return;
}
if(event.key==='Enter'&&active>=0){
event.preventDefault();
window.location.assign(items[active].href);
return;
}
if(event.key==='Escape'&&!list.hidden){
if(input.id!=='workspace-search')event.preventDefault();
close();
}
});
form.addEventListener('submit',close);
document.addEventListener('pointerdown',function(event){if(!form.contains(event.target))close()});
}
var inputs=document.querySelectorAll('form.search input[name=q],#search-query');
for(var index=0;index<inputs.length;index++)bind(inputs[index]);
})();</script>`

// progressiveEnhancementScript is the whole client budget for the workspace
// page: submit forms without losing the page, keep the composer usable, keep
// the live stream open, and re-render the message regions the server owns.
//
// The script carries no JavaScript comments on purpose. html/template elides
// them in a script context, so the bytes the browser receives would stop
// matching the Content-Security-Policy hash computed from this constant and the
// whole client would be silently blocked. Hence the length of this one.
//
// Load-bearing properties, each of which replaced a defect:
//
//   - bursts collapse into one refresh; a refresh aborts the one before it and
//     drops any response that lands after a newer one started. Ten events used
//     to issue ten concurrent scans whose responses could land out of order and
//     visibly revert the timeline.
//   - a submit takes a lock and disables its button, and success clears only
//     the exact text that was sent. Holding Enter used to post twice.
//   - the stream reports its own failure: a 401 closes an EventSource for good,
//     and the page used to keep looking live forever.
//   - every URL the client fetches must be a path on this origin. These fetches
//     carry credentials.
//   - a refresh the reader caused is not cancelled by one nobody asked for.
//     While a forced refresh is in flight a background one yields, because the
//     event that provoked it will be in the response already being fetched.
//   - a region is not re-rendered from markup identical to what it was last
//     given: that cannot add information and can destroy focus, caret, scroll
//     anchor and open disclosures. The remembered markup is cleared wherever
//     something other than refresh() writes to a region.
//
// refresh(force) is forced only for a change the reader made: it re-renders
// every message region and does not step aside for focus, because the reader is
// waiting. Somebody else's event leaves a focused region alone. A post from a
// window that is not the newest navigates to the newest instead of appending,
// because the stored message cannot appear in the window on screen.
//
// Arriving from a permalink lands on the message the link names. The link
// carries a window cursor ending just after it and a fragment naming it; the
// message is focused rather than only scrolled to, because a fragment moves the
// viewport and not the keyboard, and the mark clears on first interaction
// rather than on a timer. The composer keeps focus only when the link named
// nothing.
//
// Copying a message link is layered on a real anchor, so a browser without
// clipboard access follows the link instead of doing nothing.
//
// notify() raises a desktop notification for messages that arrived while the
// tab was not looked at. Its text comes from the timeline this client fetched
// under its own session, never from the event frame: durable payloads carry
// identifiers and no content by design. Three things must be true, each
// somebody else's decision — the preference is on, the browser granted
// permission, and Do Not Disturb is not active.
//
// A submitting control is re-enabled when the mutation finishes, not when the
// view catches up. Those are separate facts and used to be one promise: on a
// 204 the submit handler returns refresh(true) and released only after it, so a
// refresh that never settled left the control disabled for ever and the next
// click did nothing at all — no request, no error, no feedback. A CI failure
// showed exactly that, with one POST /app/conversation/create in the network
// trace where the test made two, and data-discarded-refreshes at 0 ruling out
// the refresh-discard defect recorded separately in the product gap audit. The
// composer's sending flag still waits for the end, because it guards against
// sending the same text twice and the text is not cleared until the view
// updates.
//
// This explanation lives here rather than beside the code it describes: the
// script's bytes are hashed for its Content-Security-Policy, html/template
// elides comments in script context, and an elided comment makes the served
// document disagree with the hash that permits it. There is deliberately not one
// comment inside the script for that reason.
var progressiveEnhancementScript = localTimeScript + `<script>(function(){
var topics=` + liveEventTopicsLiteral() + `;
var composer=document.getElementById('composer');
var text=document.getElementById('text');
var mentionSuggestions=document.getElementById('mention-suggestions');
var channelSuggestions=document.getElementById('channel-suggestions');
var emojiSuggestions=document.getElementById('emoji-suggestions');
var slashSuggestions=document.getElementById('slash-suggestions');
var emojiPicker=document.getElementById('emoji-picker-dialog');
var emojiPickerQuery=document.getElementById('emoji-picker-query');
var emojiPickerResults=document.getElementById('emoji-picker-results');
var emojiPickerStatus=document.getElementById('emoji-picker-status');
var emojiPickerClose=document.getElementById('emoji-picker-close');
var emojiPickerCategory=document.getElementById('emoji-picker-category');
var emojiPickerTone=document.getElementById('emoji-picker-tone');
var shortcutBrowser=document.getElementById('shortcut-browser');
var shortcutBrowserQuery=document.getElementById('shortcut-browser-query');
var shortcutBrowserResults=document.getElementById('shortcut-browser-results');
var shortcutBrowserEmpty=document.getElementById('shortcut-browser-empty');
var shortcutBrowserClose=document.getElementById('shortcut-browser-close');
var shortcutBrowserOpen=document.getElementById('open-shortcut-browser');
var uploadFile=document.getElementById('upload-file');
var uploadPreview=document.getElementById('upload-preview');
var uploadForm=document.getElementById('upload-form');
var uploadComment=document.getElementById('upload-comment');
var uploadDraftAttachments=document.getElementById('upload-draft-attachments');
var draftAttachmentInput=document.getElementById('draft-attachments');
var uploadClear=document.getElementById('upload-clear');
var clipDialog=document.getElementById('clip-recorder');
var clipTitle=document.getElementById('clip-recorder-title');
var clipStatus=document.getElementById('clip-recorder-status');
var clipPreview=document.getElementById('clip-recorder-preview');
var clipStop=document.getElementById('clip-recorder-stop');
var clipCancel=document.getElementById('clip-recorder-cancel');
var search=document.getElementById('workspace-search');
var activityLink=document.getElementById('activity-link');
var errorBox=document.getElementById('composer-error');
var actionBox=document.getElementById('action-feedback');
var status=document.getElementById('live-status');
var nav=document.getElementById('workspace-sidebar');
var navToggle=document.getElementById('nav-toggle');
var navScrim=document.getElementById('nav-scrim');
var switcher=document.getElementById('conversation-switcher');
var switcherQuery=document.getElementById('conversation-switcher-query');
var switcherClose=switcher?switcher.querySelector('.switcher-close'):null;
var browserTimezone='UTC';
try{browserTimezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'UTC'}catch(error){}
Array.prototype.forEach.call(document.querySelectorAll('[data-browser-timezone]'),function(input){input.value=browserTimezone});
var narrow=window.matchMedia?window.matchMedia('(max-width:800px)'):null;
var generation=0;
var refreshResponses=0;
var discardedRefreshes=0;
document.documentElement.setAttribute('data-refresh-responses','0');
document.documentElement.setAttribute('data-discarded-refreshes','0');
var inFlight=null;
var scheduled=null;
var appliedHTML=new WeakMap();
var forcing=0;
var draftTimer=null;
var sending=false;
var stagingFiles=false;
var draftAttachments=[];
try{draftAttachments=JSON.parse(draftAttachmentInput&&draftAttachmentInput.value||'[]');if(!Array.isArray(draftAttachments))draftAttachments=[]}catch(error){draftAttachments=[]}
var streamState='';
var mentionStart=-1;
var channelStart=-1;
var emojiStart=-1;
var emojiRequest=null;
var emojiTimer=null;
var emojiPickerTarget='composer';
var emojiReactionFormID='';
var emojiPickerTrigger=null;
var clipRecorder=null;
var clipStream=null;
var clipChunks=[];
var clipCancelled=false;
var clipLimitTimer=null;
var clipElapsedTimer=null;
var clipStartedAt=0;
var clipTrigger=null;
var clipGeneration=0;
var draftKey=composer?'sameoldchat-draft:'+composer.getAttribute('action'):'';
var applePlatform=/Mac|iPhone|iPad/.test(navigator.platform||'');
function primaryShortcut(event){return applePlatform?event.metaKey&&!event.ctrlKey:event.ctrlKey&&!event.metaKey}
function localize(root){if(window.sameoldchatLocalTimes)window.sameoldchatLocalTimes(root)}
function announce(message){if(status)status.textContent=message}
function showError(message,form){var box=form===composer?errorBox:actionBox;if(!box){window.alert(message);return}box.textContent=message;box.hidden=false;if(form===composer&&composer)composer.classList.add('is-error');box.scrollIntoView({block:'nearest'});box.focus()}
function clearError(form){var box=form===composer?errorBox:actionBox;if(!box)return;box.textContent='';box.hidden=true;if(form===composer&&composer)composer.classList.remove('is-error')}
function failure(error,form){var message=error&&error.message?String(error.message).trim():'';if(message.charAt(0)==='<')message='';if(message.length>200)message=message.slice(0,200);if(message)return message;return form===composer?'The request could not be completed. Your message was kept in the composer.':'The request could not be completed. Nothing was changed.'}
function persistDraft(){
if(!text||!draftKey)return;
try{if(text.value)localStorage.setItem(draftKey,text.value);else localStorage.removeItem(draftKey)}catch(error){}
if(draftTimer)window.clearTimeout(draftTimer);
draftTimer=window.setTimeout(function(){saveDraftRemote(false)},450);
}
function persistDraftNow(){
if(!text)return Promise.resolve();
try{if(draftKey){if(text.value)localStorage.setItem(draftKey,text.value);else localStorage.removeItem(draftKey)}}catch(error){}
return saveDraftRemote(false);
}
function saveDraftRemote(keepalive){
if(!composer||!text)return Promise.resolve();
var action=composer.getAttribute('data-draft-url');
if(!action||!ownPath(action))return Promise.resolve();
if(draftTimer){window.clearTimeout(draftTimer);draftTimer=null}
var body=new URLSearchParams();
var csrf=composer.querySelector('input[name="_csrf"]');
var thread=composer.querySelector('input[name="thread_ts"]');
body.set('_csrf',csrf?csrf.value:'');
body.set('text',text.value);
body.set('draft_attachments',JSON.stringify(draftAttachments));
if(thread)body.set('thread_ts',thread.value);
return fetch(action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin',keepalive:!!keepalive}).then(function(response){if(!response.ok)announce('Your draft has not been saved yet. Keep this tab open and try typing again.')}).catch(function(){announce('Your draft has not been saved yet. Keep this tab open and try typing again.')});
}
function replaceComposerRange(start,end,value,selectStart,selectEnd){
if(!text)return;
var before=text.value.slice(0,start);
var suffix=text.value.slice(end);
text.value=before+value+suffix;
var first=typeof selectStart==='number'?start+selectStart:start+value.length;
var last=typeof selectEnd==='number'?start+selectEnd:first;
text.focus();
text.setSelectionRange(first,last);
persistDraft();
text.dispatchEvent(new Event('input',{bubbles:true}));
}
function wrapComposerSelection(wrapper){
if(!text)return;
var start=text.selectionStart;
var end=text.selectionEnd;
var selected=text.value.slice(start,end);
if(!selected)selected='text';
replaceComposerRange(start,end,wrapper+selected+wrapper,wrapper.length,wrapper.length+selected.length);
}
function currentMention(){
if(!text)return null;
var cursor=text.selectionStart;
var before=text.value.slice(0,cursor);
var match=/(^|\s)@([^\s@<>]*)$/.exec(before);
if(!match)return null;
return{start:cursor-match[2].length-1,end:cursor,query:match[2].toLowerCase()};
}
function mentionOptions(){return mentionSuggestions?Array.prototype.slice.call(mentionSuggestions.querySelectorAll('[data-mention-user],[data-mention-group]')).filter(function(option){return !option.hidden}):[]}
function hideMentions(){
mentionStart=-1;
if(mentionSuggestions)mentionSuggestions.hidden=true;
if(text){text.setAttribute('aria-expanded','false');text.removeAttribute('aria-activedescendant')}
}
function updateMentions(){
if(!mentionSuggestions||!text){hideMentions();return false}
var mention=currentMention();
if(!mention){hideMentions();return false}
mentionStart=mention.start;
var visible=0;
var options=mentionSuggestions.querySelectorAll('[data-mention-user],[data-mention-group]');
for(var index=0;index<options.length;index++){
var search=(options[index].getAttribute('data-mention-search')||options[index].getAttribute('data-mention-name')||'').toLowerCase();
var show=visible<8&&search.indexOf(mention.query)!==-1;
options[index].hidden=!show;
options[index].setAttribute('aria-selected',show&&visible===0?'true':'false');
if(show){options[index].id='mention-option-'+visible;visible++}else{options[index].removeAttribute('id')}
}
mentionSuggestions.hidden=visible===0;
text.setAttribute('aria-expanded',visible?'true':'false');
if(visible)text.setAttribute('aria-activedescendant','mention-option-0');else text.removeAttribute('aria-activedescendant');
return visible>0;
}
function chooseMention(option){
if(!text||!option)return;
var mention=currentMention();
var start=mention?mention.start:mentionStart;
if(start<0)start=text.selectionStart;
var group=option.getAttribute('data-mention-group');
var reference=group?'<!subteam^'+group+'>':'<@'+option.getAttribute('data-mention-user')+'>';
replaceComposerRange(start,text.selectionStart,reference+' ',undefined,undefined);
hideMentions();
var details=option.closest('details');
if(details)details.open=false;
}
function currentChannel(){
if(!text)return null;
var cursor=text.selectionStart;
var before=text.value.slice(0,cursor);
var match=/(^|\s)#([^\s#<>]*)$/.exec(before);
if(!match)return null;
return{start:cursor-match[2].length-1,end:cursor,query:match[2].toLowerCase()};
}
function channelOptions(){return channelSuggestions?Array.prototype.slice.call(channelSuggestions.querySelectorAll('[data-channel-id]')).filter(function(option){return !option.hidden}):[]}
function hideChannels(){
channelStart=-1;
if(channelSuggestions)channelSuggestions.hidden=true;
if(text){text.setAttribute('aria-expanded','false');text.removeAttribute('aria-activedescendant')}
}
function updateChannels(){
if(!channelSuggestions||!text){hideChannels();return false}
var channel=currentChannel();
if(!channel){hideChannels();return false}
channelStart=channel.start;
var visible=0;
var options=channelSuggestions.querySelectorAll('[data-channel-id]');
for(var index=0;index<options.length;index++){
var name=(options[index].getAttribute('data-channel-name')||'').toLowerCase();
var show=visible<8&&name.indexOf(channel.query)!==-1;
options[index].hidden=!show;
options[index].setAttribute('aria-selected',show&&visible===0?'true':'false');
if(show){options[index].id='channel-option-'+visible;visible++}else{options[index].removeAttribute('id')}
}
channelSuggestions.hidden=visible===0;
text.setAttribute('aria-expanded',visible?'true':'false');
if(visible)text.setAttribute('aria-activedescendant','channel-option-0');else text.removeAttribute('aria-activedescendant');
return visible>0;
}
function chooseChannel(option){
if(!text||!option)return;
var channel=currentChannel();
var start=channel?channel.start:channelStart;
if(start<0)start=text.selectionStart;
replaceComposerRange(start,text.selectionStart,'<#'+option.getAttribute('data-channel-id')+'> ',undefined,undefined);
hideChannels();
}
function currentEmoji(){
if(!text)return null;
var cursor=text.selectionStart;
var before=text.value.slice(0,cursor);
var match=/(^|\s):([a-zA-Z0-9_+\-]*)$/.exec(before);
if(!match)return null;
return{start:cursor-match[2].length-1,end:cursor,query:match[2].toLowerCase()};
}
function emojiOptions(region){return region?Array.prototype.slice.call(region.querySelectorAll('[data-emoji-name]')).filter(function(option){return !option.hidden}):[]}
function hideEmojiSuggestions(){
emojiStart=-1;
if(emojiSuggestions){emojiSuggestions.hidden=true;emojiSuggestions.textContent=''}
if(text){text.setAttribute('aria-expanded','false');text.removeAttribute('aria-activedescendant')}
}
function emojiOption(option,index){
var button=document.createElement('button');
button.type='button';
button.setAttribute('role','option');
button.setAttribute('data-emoji-name',option.name||'');
button.setAttribute('aria-label',':'+(option.name||'')+':');
button.setAttribute('aria-selected',index===0?'true':'false');
if(option.skin_tones)button.setAttribute('data-skin-tones','true');
var visual;
if(option.image_url){visual=document.createElement('img');visual.className='custom-emoji';visual.src=option.image_url;visual.alt=''}
else{visual=document.createElement('span');visual.className='emoji-glyph';visual.setAttribute('aria-hidden','true');visual.textContent=(option.display||'')+(option.skin_tones&&emojiPickerTone&&emojiPickerTone.value?String.fromCodePoint(0x1F3FB+parseInt(emojiPickerTone.value,10)-2):'')}
var label=document.createElement('small');
label.textContent=':'+option.name+':';
button.appendChild(visual);
button.appendChild(label);
return button;
}
function loadEmojiOptions(query,region,statusNode){
if(!region)return Promise.resolve([]);
if(emojiRequest)emojiRequest.abort();
emojiRequest=window.AbortController?new AbortController():null;
var options={credentials:'same-origin'};
if(emojiRequest)options.signal=emojiRequest.signal;
if(statusNode)statusNode.textContent='Loading emoji…';
var category=region===emojiPickerResults&&emojiPickerCategory?emojiPickerCategory.value:'';
var recent=[];
try{recent=JSON.parse(localStorage.getItem('sameoldchat-recent-emoji')||'[]');if(!Array.isArray(recent))recent=[]}catch(error){recent=[]}
var parameters=new URLSearchParams({q:query||''});
if(category)parameters.set('category',category);
if(recent.length)parameters.set('recent',recent.slice(0,24).join(','));
return fetch('/app/emoji/options?'+parameters.toString(),options).then(function(response){
if(!response.ok)throw new Error('Emoji could not be loaded.');
return response.json();
}).then(function(payload){
region.textContent='';
var values=payload&&Array.isArray(payload.options)?payload.options:[];
if(emojiPickerCategory&&payload&&Array.isArray(payload.categories)&&emojiPickerCategory.options.length<=3){
for(var categoryIndex=0;categoryIndex<payload.categories.length;categoryIndex++){
var categoryName=String(payload.categories[categoryIndex]||'');
if(!categoryName||Array.prototype.some.call(emojiPickerCategory.options,function(item){return item.value===categoryName}))continue;
var categoryOption=document.createElement('option');categoryOption.value=categoryName;categoryOption.textContent=categoryName;emojiPickerCategory.appendChild(categoryOption);
}}
for(var index=0;index<values.length;index++){
var item=document.createElement('li');
item.appendChild(emojiOption(values[index],index));
region.appendChild(item);
}
if(statusNode)statusNode.textContent=values.length?(values.length+' emoji shown.'):'No matching emoji.';
return values;
}).catch(function(error){
if(error&&error.name==='AbortError')return[];
region.textContent='';
if(statusNode)statusNode.textContent='Emoji could not be loaded. Try again.';
return[];
});
}
function updateEmojiSuggestions(){
if(!emojiSuggestions||!text){hideEmojiSuggestions();return false}
var emoji=currentEmoji();
if(!emoji){hideEmojiSuggestions();return false}
emojiStart=emoji.start;
emojiSuggestions.hidden=false;
if(emojiTimer)window.clearTimeout(emojiTimer);
emojiTimer=window.setTimeout(function(){
loadEmojiOptions(emoji.query,emojiSuggestions,null).then(function(values){
if(!currentEmoji()){hideEmojiSuggestions();return}
emojiSuggestions.hidden=values.length===0;
text.setAttribute('aria-expanded',values.length?'true':'false');
if(values.length){var first=emojiOptions(emojiSuggestions)[0];if(first){first.id='emoji-option-0';text.setAttribute('aria-activedescendant','emoji-option-0')}}else text.removeAttribute('aria-activedescendant');
});
},100);
return true;
}
function chooseEmoji(option,inline){
if(!option)return;
var name=option.getAttribute('data-emoji-name')||'';
if(!name)return;
try{var recent=JSON.parse(localStorage.getItem('sameoldchat-recent-emoji')||'[]');if(!Array.isArray(recent))recent=[];recent=recent.filter(function(value){return value!==name});recent.unshift(name);localStorage.setItem('sameoldchat-recent-emoji',JSON.stringify(recent.slice(0,24)))}catch(error){}
var tone=option.hasAttribute('data-skin-tones')&&emojiPickerTone?emojiPickerTone.value:'';
var reactionName=name+(tone?'::skin-tone-'+tone:'');
if(inline&&text){
var emoji=currentEmoji();
var start=emoji?emoji.start:emojiStart;
if(start<0)start=text.selectionStart;
replaceComposerRange(start,text.selectionStart,':'+reactionName+': ',undefined,undefined);
hideEmojiSuggestions();
return;
}
if(emojiPickerTarget==='reaction'&&emojiReactionFormID){
var reactionForm=document.getElementById(emojiReactionFormID);
if(!reactionForm){if(emojiPicker&&emojiPicker.open)emojiPicker.close();announce('That message changed while the picker was open. Open its reaction picker again.');return}
var input=reactionForm.querySelector('input[name=name]');
if(input)input.value=reactionName;
if(emojiPicker&&emojiPicker.open)emojiPicker.close();
if(typeof reactionForm.requestSubmit==='function')reactionForm.requestSubmit();else reactionForm.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));
return;
}
if(text){
replaceComposerRange(text.selectionStart,text.selectionEnd,':'+reactionName+':',undefined,undefined);
if(emojiPicker&&emojiPicker.open)emojiPicker.close();
}
}
function openEmojiPicker(control){
if(!emojiPicker||typeof emojiPicker.showModal!=='function')return false;
emojiPickerTarget=control&&control.getAttribute('data-emoji-target')||'composer';
emojiReactionFormID='';
if(emojiPickerTarget==='reaction'){emojiReactionFormID=control.getAttribute('data-reaction-form')||'';if(!emojiReactionFormID||!document.getElementById(emojiReactionFormID))return false}
emojiPickerTrigger=control;
if(!emojiPicker.open)emojiPicker.showModal();
if(emojiPickerQuery){emojiPickerQuery.value='';emojiPickerQuery.focus()}
loadEmojiOptions('',emojiPickerResults,emojiPickerStatus);
return true;
}
function currentSlash(){
if(!text||text.selectionStart!==text.selectionEnd)return null;
var value=text.value.slice(0,text.selectionStart);
if(!/^\/[^\s]*$/.test(value)||text.selectionStart!==text.value.length)return null;
return{query:value.toLowerCase()};
}
function slashOptions(){return slashSuggestions?Array.prototype.slice.call(slashSuggestions.querySelectorAll('[data-slash-command]')).filter(function(option){return !option.hidden}):[]}
function hideSlashes(){
if(slashSuggestions)slashSuggestions.hidden=true;
if(text){text.setAttribute('aria-expanded','false');text.removeAttribute('aria-activedescendant')}
}
function updateSlashes(){
if(!slashSuggestions||!text){hideSlashes();return false}
var slash=currentSlash();
if(!slash){hideSlashes();return false}
hideMentions();
var visible=0;
var options=slashSuggestions.querySelectorAll('[data-slash-command]');
for(var index=0;index<options.length;index++){
var search=(options[index].getAttribute('data-slash-search')||'').toLowerCase();
var show=visible<10&&search.indexOf(slash.query)!==-1;
options[index].hidden=!show;
options[index].setAttribute('aria-selected',show&&visible===0?'true':'false');
if(show){options[index].id='slash-option-'+visible;visible++}else{options[index].removeAttribute('id')}
}
slashSuggestions.hidden=visible===0;
text.setAttribute('aria-expanded',visible?'true':'false');
if(visible)text.setAttribute('aria-activedescendant','slash-option-0');else text.removeAttribute('aria-activedescendant');
return visible>0;
}
function updateAutocomplete(){
if(updateSlashes()){hideMentions();hideChannels();hideEmojiSuggestions();return}
if(updateMentions()){hideChannels();hideEmojiSuggestions();return}
if(updateChannels()){hideEmojiSuggestions();return}
updateEmojiSuggestions();
}
function chooseSlash(option){
if(!text||!option)return;
replaceComposerRange(0,text.value.length,(option.getAttribute('data-slash-command')||'')+' ',undefined,undefined);
hideSlashes();
}
function openSwitcher(){
if(!switcher||typeof switcher.showModal!=='function')return false;
if(!switcher.open)switcher.showModal();
if(switcherQuery){switcherQuery.value='';filterSwitcher();switcherQuery.focus()}
return true;
}
function filterSwitcher(){
if(!switcher||!switcherQuery)return;
var query=switcherQuery.value.trim().toLowerCase();
var links=switcher.querySelectorAll('[data-conversation-name]');
for(var index=0;index<links.length;index++){
var show=(links[index].getAttribute('data-conversation-name')||'').toLowerCase().indexOf(query)!==-1;
var item=links[index].closest('li');
if(item)item.hidden=!show;
}
}
function setNav(open,focus){
if(!nav||!navToggle||!navScrim)return;
var mobile=!!(narrow&&narrow.matches);
open=mobile&&open;
nav.classList.toggle('is-open',open);
navScrim.classList.toggle('is-open',open);
navToggle.setAttribute('aria-expanded',open?'true':'false');
navToggle.setAttribute('aria-label',open?'Close navigation':'Open navigation');
if(mobile&&!open)nav.setAttribute('inert','');else nav.removeAttribute('inert');
if(open&&focus){var current=nav.querySelector('[aria-current="page"]')||nav.querySelector('.side-link');if(current)current.focus()}
}
function navFocusables(){
if(!nav)return[];
return Array.prototype.slice.call(nav.querySelectorAll('a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),summary,[tabindex]:not([tabindex="-1"])')).filter(function(item){return !!(item.offsetParent||item.getClientRects().length)});
}
function ownPath(value){return typeof value==='string'&&value.charAt(0)==='/'&&value.charAt(1)!=='/'}
function shown(region){return !!(region.offsetParent||region.getClientRects().length)}
function atBottom(region){return region.scrollHeight-region.scrollTop-region.clientHeight<48}
function toBottom(region){if(region)region.scrollTop=region.scrollHeight}
function messageItems(region){return region?Array.prototype.slice.call(region.querySelectorAll('.message')).filter(shown):[]}
function focusMessage(message){if(!message)return false;try{message.focus({preventScroll:true})}catch(error){message.focus()}message.scrollIntoView({block:'nearest'});return true}
function messageDetails(message,label){
var details=message?message.querySelectorAll('.message-actions details'):null;
for(var index=0;details&&index<details.length;index++){var summary=details[index].querySelector('summary');if(summary&&summary.textContent.trim()===label)return details[index]}
return null;
}
function revealDisclosure(details){
var node=details;
while(node){node.open=true;node=node.parentElement?node.parentElement.closest('details'):null;}
}
function openMessageDetails(message,label,control){
var details=messageDetails(message,label);
if(!details)return false;
revealDisclosure(details);
var target=details.querySelector(control);
if(target)target.focus();
return true;
}
function notify(arrived){
var shell=document.querySelector('[data-browser-notifications]');
if(!shell||shell.getAttribute('data-browser-notifications')!=='true')return;
if(shell.getAttribute('data-notifications-paused')==='true')return;
if(!('Notification' in window)||Notification.permission!=='granted')return;
if(!document.hidden)return;
var channel=shell.getAttribute('data-channel-name')||'this workspace';
try{new Notification(arrived===1?'1 new message in '+channel:arrived+' new messages in '+channel,{tag:'sameoldchat-'+channel})}catch(error){}
}
function regions(force){return document.querySelectorAll(force?'[data-fragment]':'[data-fragment][data-live="true"]')}
function messageCount(){return document.querySelectorAll('[data-fragment] .message').length}
function refresh(force){
if(!force&&forcing>0)return Promise.resolve([]);
var candidates=[];
var live=regions(force);
for(var candidateIndex=0;candidateIndex<live.length;candidateIndex++){
var candidate=live[candidateIndex];
var candidateTarget=candidate.getAttribute('data-fragment');
if(!ownPath(candidateTarget)||!shown(candidate))continue;
var candidateFocused=!!(document.activeElement&&candidate.contains(document.activeElement));
if(candidateFocused&&!force)continue;
candidates.push({region:candidate,target:candidateTarget,focused:candidateFocused});
}
if(!candidates.length)return Promise.resolve([]);
if(force)forcing++;
var settle=function(){if(force&&forcing>0)forcing--};
generation++;
var token=generation;
if(inFlight){inFlight.abort();inFlight=null}
var controller=window.AbortController?new AbortController():null;
inFlight=controller;
var pending=[];
for(var index=0;index<candidates.length;index++){(function(candidate){
var region=candidate.region;
var target=candidate.target;
var focused=candidate.focused;
var stick=atBottom(region);
var options={headers:{'HX-Request':'true'},credentials:'same-origin'};
if(controller)options.signal=controller.signal;
var activeMessage=focused?document.activeElement.closest('.message'):null;
var activeMessageID=activeMessage?activeMessage.getAttribute('data-message-id'):'';
pending.push(fetch(target,options).then(function(response){if(!response.ok)throw new Error('The conversation could not be refreshed.');return response.text()}).then(function(html){
refreshResponses++;
document.documentElement.setAttribute('data-refresh-responses',String(refreshResponses));
if(token!==generation){discardedRefreshes++;document.documentElement.setAttribute('data-discarded-refreshes',String(discardedRefreshes));return}
if(!force&&document.activeElement&&region.contains(document.activeElement))return;
if(appliedHTML.get(region)===html)return;
appliedHTML.set(region,html);
region.innerHTML=html;
localize(region);
if(stick)toBottom(region);
if(activeMessageID){var items=messageItems(region);var restored=items.find(function(item){return item.getAttribute('data-message-id')===activeMessageID});if(focusMessage(restored))return}
if(focused&&region.hasAttribute('tabindex'))region.focus();
}));
})(candidates[index])}
return Promise.all(pending).then(function(value){settle();if(inFlight===controller)inFlight=null;return value},function(error){settle();throw error});
}
function scheduleRefresh(){
if(scheduled)return;
scheduled=window.setTimeout(function(){
scheduled=null;
var behind=document.querySelectorAll('[data-fragment]:not([data-live="true"])').length>0;
var before=messageCount();
refresh(false).then(function(){
var arrived=messageCount()-before;
if(arrived>0){announce(arrived===1?'1 new message.':arrived+' new messages.');notify(arrived);return}
if(behind)announce('New activity is available in this conversation.');
}).catch(function(error){if(error&&error.name==='AbortError')return;announce('New activity could not be loaded. Reload the page.')});
},250);
}
function submitQuietly(form){
var action=form.getAttribute('hx-post');
if(!ownPath(action))return;
fetch(action,{method:'POST',body:new FormData(form),headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error('Unread state could not be saved.');form.hidden=true}).catch(function(){announce('Unread state could not be saved. Messages are still available.')});
}
document.addEventListener('click',function(event){
var copyLink=event.target.closest?event.target.closest('[data-copy-link]'):null;
if(copyLink){
if(navigator.clipboard&&navigator.clipboard.writeText){
event.preventDefault();
var absolute=new URL(copyLink.getAttribute('data-copy-link'),window.location.origin).toString();
navigator.clipboard.writeText(absolute).then(function(){announce('Link copied.')}).catch(function(){window.location.assign(copyLink.getAttribute('href'))});
}
return;
}
var control=event.target.closest?event.target.closest('[data-wrap],[data-insert],[data-mention-user],[data-mention-group],[data-channel-id],[data-slash-command],[data-emoji-name],[data-open-emoji-picker]'):null;
if(control&&control.hasAttribute('data-open-emoji-picker')){if(openEmojiPicker(control))event.preventDefault();return}
if(control&&control.hasAttribute('data-emoji-name')){chooseEmoji(control,!!(emojiSuggestions&&emojiSuggestions.contains(control)));return}
if(!control||!composer||!composer.contains(control)||!text)return;
if(control.hasAttribute('data-mention-user')||control.hasAttribute('data-mention-group')){chooseMention(control);return}
if(control.hasAttribute('data-channel-id')){chooseChannel(control);return}
if(control.hasAttribute('data-slash-command')){chooseSlash(control);return}
var start=text.selectionStart;
var end=text.selectionEnd;
var wrapper=control.getAttribute('data-wrap');
if(wrapper!==null){
wrapComposerSelection(wrapper);
return;
}
var inserted=control.getAttribute('data-insert');
if(inserted===null)return;
var offset=parseInt(control.getAttribute('data-select-offset'),10);
var length=parseInt(control.getAttribute('data-select-length'),10);
replaceComposerRange(start,end,inserted,isNaN(offset)?undefined:offset,isNaN(offset)||isNaN(length)?undefined:offset+length);
var details=control.closest('details');
if(details)details.open=false;
});
if(text&&composer){
if(!text.value&&draftKey){try{var saved=localStorage.getItem(draftKey);if(saved)text.value=saved}catch(error){}}
persistDraft();
text.addEventListener('input',function(){persistDraft();updateAutocomplete()});
text.addEventListener('click',updateAutocomplete);
window.addEventListener('pagehide',function(){saveDraftRemote(true)});
window.addEventListener('beforeunload',function(event){if(stagingFiles){event.preventDefault();event.returnValue=''}});
}
if(switcherQuery)switcherQuery.addEventListener('input',filterSwitcher);
if(switcherClose)switcherClose.addEventListener('click',function(){switcher.close()});
if(emojiPickerQuery)emojiPickerQuery.addEventListener('input',function(){
if(emojiTimer)window.clearTimeout(emojiTimer);
emojiTimer=window.setTimeout(function(){loadEmojiOptions(emojiPickerQuery.value,emojiPickerResults,emojiPickerStatus)},100);
});
if(emojiPickerCategory)emojiPickerCategory.addEventListener('change',function(){loadEmojiOptions(emojiPickerQuery?emojiPickerQuery.value:'',emojiPickerResults,emojiPickerStatus)});
if(emojiPickerTone)emojiPickerTone.addEventListener('change',function(){try{localStorage.setItem('sameoldchat-emoji-tone',emojiPickerTone.value)}catch(error){}loadEmojiOptions(emojiPickerQuery?emojiPickerQuery.value:'',emojiPickerResults,emojiPickerStatus)});
if(emojiPickerTone){try{emojiPickerTone.value=localStorage.getItem('sameoldchat-emoji-tone')||''}catch(error){}}
if(emojiPickerQuery)emojiPickerQuery.addEventListener('keydown',function(event){
var options=emojiOptions(emojiPickerResults);
if(!options.length)return;
var selected=options.findIndex(function(option){return option.getAttribute('aria-selected')==='true'});
if(event.key==='ArrowDown'||event.key==='ArrowUp'){
event.preventDefault();
if(selected<0)selected=event.key==='ArrowDown'?0:options.length-1;
for(var index=0;index<options.length;index++)options[index].setAttribute('aria-selected',index===selected?'true':'false');
options[selected].scrollIntoView({block:'nearest'});
options[selected].focus();
return;
}
if(event.key==='Enter'){event.preventDefault();chooseEmoji(options[selected<0?0:selected],false)}
});
if(emojiPickerResults)emojiPickerResults.addEventListener('keydown',function(event){
var options=emojiOptions(emojiPickerResults);
var selected=options.indexOf(document.activeElement);
if(event.key==='ArrowDown'||event.key==='ArrowUp'){
event.preventDefault();
if(selected<0)selected=0;else selected=event.key==='ArrowDown'?(selected+1)%options.length:(selected+options.length-1)%options.length;
for(var index=0;index<options.length;index++)options[index].setAttribute('aria-selected',index===selected?'true':'false');
if(options[selected])options[selected].focus();
return;
}
if(event.key==='Escape'&&emojiPickerQuery){event.preventDefault();emojiPickerQuery.focus()}
});
if(emojiPickerClose)emojiPickerClose.addEventListener('click',function(){emojiPicker.close()});
if(emojiPicker)emojiPicker.addEventListener('close',function(){
if(emojiPickerTrigger&&document.contains(emojiPickerTrigger))emojiPickerTrigger.focus();
emojiPickerTarget='composer';emojiReactionFormID='';emojiPickerTrigger=null;
});
function filterShortcuts(){
if(!shortcutBrowserResults)return;
var query=(shortcutBrowserQuery.value||'').trim().toLowerCase();var shown=0;
Array.prototype.forEach.call(shortcutBrowserResults.children,function(item){var search=(item.getAttribute('data-shortcut-search')||'').toLowerCase();var visible=!query||search.indexOf(query)!==-1;item.hidden=!visible;if(visible)shown++});
if(shortcutBrowserEmpty)shortcutBrowserEmpty.hidden=shown!==0;
}
function closeShortcutBrowser(){if(shortcutBrowser&&shortcutBrowser.open)shortcutBrowser.close()}
if(shortcutBrowserOpen)shortcutBrowserOpen.addEventListener('click',function(){shortcutBrowser.showModal();shortcutBrowserQuery.value='';filterShortcuts();shortcutBrowserQuery.focus()});
if(shortcutBrowserQuery)shortcutBrowserQuery.addEventListener('input',filterShortcuts);
if(shortcutBrowserClose)shortcutBrowserClose.addEventListener('click',closeShortcutBrowser);
if(shortcutBrowserResults)shortcutBrowserResults.addEventListener('click',function(event){var choice=event.target.closest('[data-browser-command]');if(!choice)return;var command=choice.getAttribute('data-browser-command');var start=text.selectionStart;var end=text.selectionEnd;replaceComposerRange(start,end,command+' ',undefined,undefined);closeShortcutBrowser()});
if(shortcutBrowser)shortcutBrowser.addEventListener('click',function(event){if(event.target===shortcutBrowser)closeShortcutBrowser()});
if(shortcutBrowser)shortcutBrowser.addEventListener('close',function(){if(shortcutBrowserOpen)shortcutBrowserOpen.focus()});
function formatFileSize(value){var size=value;var unit='B';if(size>=1048576){size=size/1048576;unit='MiB'}else if(size>=1024){size=size/1024;unit='KiB'}return(unit==='B'?size:String(Math.round(size*10)/10))+' '+unit}
function syncDraftAttachments(){
var encoded=JSON.stringify(draftAttachments);
if(draftAttachmentInput)draftAttachmentInput.value=encoded;
if(uploadDraftAttachments)uploadDraftAttachments.value=encoded;
}
function updateUploadPreview(){
if(!uploadFile||!uploadPreview)return;
var files=uploadFile.files?Array.prototype.slice.call(uploadFile.files):[];
uploadPreview.textContent='';
if(!draftAttachments.length&&!files.length){uploadPreview.textContent='';if(uploadClear)uploadClear.hidden=true;if(text)text.required=true;return}
draftAttachments.forEach(function(file,index){
var item=document.createElement('span');item.className='staged-file';item.appendChild(document.createTextNode((file.name||'Staged file')+' · '+formatFileSize(file.size||0)+' '));
if(draftAttachments.length>1&&index>0){var earlier=document.createElement('button');earlier.type='button';earlier.setAttribute('data-move-draft-attachment',String(index));earlier.setAttribute('data-move-direction','-1');earlier.setAttribute('aria-label','Move '+(file.name||'staged file')+' earlier');earlier.textContent='←';item.appendChild(earlier)}
if(draftAttachments.length>1&&index<draftAttachments.length-1){var later=document.createElement('button');later.type='button';later.setAttribute('data-move-draft-attachment',String(index));later.setAttribute('data-move-direction','1');later.setAttribute('aria-label','Move '+(file.name||'staged file')+' later');later.textContent='→';item.appendChild(later)}
var remove=document.createElement('button');remove.type='button';remove.setAttribute('data-remove-draft-attachment',String(index));remove.setAttribute('aria-label','Remove '+(file.name||'staged file'));remove.textContent='Remove';item.appendChild(remove);uploadPreview.appendChild(item);
});
files.forEach(function(file){var item=document.createElement('span');item.className='staged-file';item.textContent=(file.name||'Pasted file')+' · '+formatFileSize(file.size)+(stagingFiles?' · uploading':'');uploadPreview.appendChild(item)});
if(uploadClear)uploadClear.hidden=false;

if(text)text.required=false;
}
function closeComposerPlus(){
var menu=document.querySelector('.composer-plus');
if(menu)menu.open=false;
}
function stageSelectedFiles(){
if(!uploadForm||!uploadFile||!uploadFile.files||!uploadFile.files.length||stagingFiles)return Promise.resolve(false);
if(draftAttachments.length+uploadFile.files.length>10){showError('A draft can contain up to ten staged files.',composer);return Promise.resolve(false)}
stagingFiles=true;clearError(composer);updateUploadPreview();
if(uploadComment)uploadComment.value=text?text.value:'';
syncDraftAttachments();
var body=new FormData(uploadForm);
return fetch(uploadForm.getAttribute('action'),{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){
if(!response.ok)return response.text().then(function(body){throw new Error(body)});
return response.json();
}).then(function(result){
draftAttachments=result&&Array.isArray(result.attachments)?result.attachments:draftAttachments;
syncDraftAttachments();uploadFile.value='';stagingFiles=false;updateUploadPreview();
return persistDraftNow().then(function(){
announce(draftAttachments.length===1?'One file is saved with this draft.':draftAttachments.length+' files are saved with this draft.');
return true});
}).catch(function(error){stagingFiles=false;updateUploadPreview();showError(failure(error,composer),composer);return false});
}
function stageFiles(fileList){
if(!uploadFile||!fileList||!fileList.length)return false;
if(!window.DataTransfer){announce('Choose pasted files with Attach files in this browser.');return false}
var transfer=new DataTransfer();var existing=uploadFile.files?Array.prototype.slice.call(uploadFile.files):[];
existing.concat(Array.prototype.slice.call(fileList)).slice(0,10).forEach(function(file){transfer.items.add(file)});
uploadFile.files=transfer.files;updateUploadPreview();stageSelectedFiles();return true;
}
function clearClipTimers(){
if(clipLimitTimer)window.clearTimeout(clipLimitTimer);
if(clipElapsedTimer)window.clearInterval(clipElapsedTimer);
clipLimitTimer=null;clipElapsedTimer=null;
}
function releaseClipStream(){
if(clipStream){clipStream.getTracks().forEach(function(track){track.stop()});clipStream=null}
if(clipPreview){clipPreview.pause();clipPreview.srcObject=null}
}
function clipClock(seconds){return Math.floor(seconds/60)+':'+String(seconds%60).padStart(2,'0')}
function updateClipElapsed(kind){
if(!clipStatus||!clipStartedAt)return;
var elapsed=Math.min(300,Math.floor((Date.now()-clipStartedAt)/1000));
clipStatus.textContent='Recording '+kind+' · '+clipClock(elapsed)+' / 5:00';
}
function supportedClipMime(kind){
var choices=kind==='video'?['video/webm;codecs=vp9,opus','video/webm;codecs=vp8,opus','video/mp4']:['audio/webm;codecs=opus','audio/webm','audio/mp4','audio/ogg;codecs=opus'];
for(var index=0;index<choices.length;index++)if(!MediaRecorder.isTypeSupported||MediaRecorder.isTypeSupported(choices[index]))return choices[index];
return '';
}
function clipExtension(type){if(type.indexOf('mp4')!==-1)return'mp4';if(type.indexOf('ogg')!==-1)return'ogg';return'webm'}
function closeClipDialog(){
if(clipDialog&&clipDialog.open)clipDialog.close();
}
function cancelClip(){
clipGeneration++;clipCancelled=true;clearClipTimers();
if(clipRecorder&&clipRecorder.state!=='inactive'){clipRecorder.stop();return}
releaseClipStream();closeClipDialog();
}
function startClip(kind,trigger){
if(!clipDialog||!uploadFile)return;
clearError(composer);
if(!window.MediaRecorder||!navigator.mediaDevices||!navigator.mediaDevices.getUserMedia){showError('This browser cannot record clips. Attach an audio or video file instead.',composer);return}
var generation=++clipGeneration;
clipTrigger=trigger;clipCancelled=false;clipChunks=[];clipRecorder=null;
if(clipTitle)clipTitle.textContent=kind==='video'?'Record a video clip':'Record an audio clip';
if(clipStatus)clipStatus.textContent=kind==='video'?'Requesting camera and microphone access…':'Requesting microphone access…';
if(clipPreview){clipPreview.hidden=kind!=='video';clipPreview.srcObject=null}
if(clipStop)clipStop.disabled=true;
clipDialog.showModal();
navigator.mediaDevices.getUserMedia(kind==='video'?{audio:true,video:true}:{audio:true}).then(function(stream){
if(generation!==clipGeneration){stream.getTracks().forEach(function(track){track.stop()});return}
clipStream=stream;
if(kind==='video'&&clipPreview){clipPreview.srcObject=stream;clipPreview.play().catch(function(){})}
var mime=supportedClipMime(kind);var options=mime?{mimeType:mime}:undefined;
try{clipRecorder=new MediaRecorder(stream,options)}catch(error){releaseClipStream();closeClipDialog();showError('Recording could not start in this browser. Attach an audio or video file instead.',composer);return}
clipRecorder.addEventListener('dataavailable',function(event){if(event.data&&event.data.size)clipChunks.push(event.data)});
clipRecorder.addEventListener('error',function(){clipCancelled=true;showError('The clip recording failed. No attachment was added.',composer)});
clipRecorder.addEventListener('stop',function(){
clearClipTimers();releaseClipStream();
var recorderType=clipRecorder&&clipRecorder.mimeType?clipRecorder.mimeType:mime;
clipRecorder=null;closeClipDialog();
if(clipCancelled||!clipChunks.length)return;
var blob=new Blob(clipChunks,{type:recorderType});
var stamp=new Date().toISOString().replace(/[:.]/g,'-');
var file=new File([blob],kind+'-clip-'+stamp+'.'+clipExtension(recorderType),{type:recorderType,lastModified:Date.now()});
if(stageFiles([file]))announce((kind==='video'?'Video':'Audio')+' clip staged. Add a message or send when ready.');
else showError('The recorded clip could not be staged. Attach an audio or video file instead.',composer);
});
clipRecorder.start(1000);clipStartedAt=Date.now();
if(clipStop)clipStop.disabled=false;
updateClipElapsed(kind);
clipElapsedTimer=window.setInterval(function(){updateClipElapsed(kind)},1000);
clipLimitTimer=window.setTimeout(function(){if(clipRecorder&&clipRecorder.state!=='inactive'){announce('Five-minute clip limit reached.');clipRecorder.stop()}},300000);
}).catch(function(error){
if(generation!==clipGeneration)return;
releaseClipStream();closeClipDialog();
var denied=error&&(error.name==='NotAllowedError'||error.name==='SecurityError');
showError(denied?'Microphone or camera permission was denied. Allow access or attach a file instead.':'The microphone or camera is unavailable. Attach an audio or video file instead.',composer);
});
}
Array.prototype.forEach.call(document.querySelectorAll('[data-record-clip]'),function(button){button.addEventListener('click',function(){startClip(button.getAttribute('data-record-clip'),button)})});
if(clipStop)clipStop.addEventListener('click',function(){if(clipRecorder&&clipRecorder.state!=='inactive')clipRecorder.stop()});
if(clipCancel)clipCancel.addEventListener('click',cancelClip);
if(clipDialog)clipDialog.addEventListener('cancel',function(event){event.preventDefault();cancelClip()});
if(clipDialog)clipDialog.addEventListener('click',function(event){if(event.target===clipDialog)cancelClip()});
if(clipDialog)clipDialog.addEventListener('close',function(){if(clipTrigger&&document.contains(clipTrigger))clipTrigger.focus();clipTrigger=null});
if(uploadFile){uploadFile.addEventListener('change',function(){updateUploadPreview();stageSelectedFiles()});syncDraftAttachments();updateUploadPreview()}
var composerAttach=document.getElementById('composer-attach');
if(composerAttach&&uploadFile){composerAttach.addEventListener('click',function(){closeComposerPlus();uploadFile.click()})}
var shortcutOpener=document.getElementById('open-shortcut-browser');
if(shortcutOpener)shortcutOpener.addEventListener('click',closeComposerPlus);
if(uploadForm)uploadForm.addEventListener('submit',function(event){event.preventDefault();stageSelectedFiles()});
if(uploadClear)uploadClear.addEventListener('click',function(){uploadFile.value='';draftAttachments=[];syncDraftAttachments();updateUploadPreview();persistDraftNow().then(function(){announce('Staged files removed from the draft.')});if(text)text.focus()});
if(uploadPreview)uploadPreview.addEventListener('click',function(event){var remove=event.target.closest('[data-remove-draft-attachment]');if(!remove)return;var index=Number(remove.getAttribute('data-remove-draft-attachment'));if(index<0||index>=draftAttachments.length)return;var name=draftAttachments[index].name||'Staged file';draftAttachments.splice(index,1);syncDraftAttachments();updateUploadPreview();persistDraftNow().then(function(){announce(name+' removed from the draft.')});if(text)text.focus()});
if(uploadPreview)uploadPreview.addEventListener('click',function(event){var move=event.target.closest('[data-move-draft-attachment]');if(!move)return;var index=Number(move.getAttribute('data-move-draft-attachment'));var target=index+Number(move.getAttribute('data-move-direction'));if(index<0||index>=draftAttachments.length||target<0||target>=draftAttachments.length)return;var moved=draftAttachments[index];draftAttachments[index]=draftAttachments[target];draftAttachments[target]=moved;syncDraftAttachments();updateUploadPreview();persistDraftNow().then(function(){announce((moved.name||'Staged file')+' moved.')})});
if(text&&uploadFile){
text.addEventListener('paste',function(event){var files=event.clipboardData&&event.clipboardData.files;if(files&&files.length&&stageFiles(files))event.preventDefault()});
composer.addEventListener('dragover',function(event){if(event.dataTransfer&&event.dataTransfer.types&&Array.prototype.indexOf.call(event.dataTransfer.types,'Files')!==-1){event.preventDefault();composer.classList.add('is-dragging')}});
composer.addEventListener('dragleave',function(){composer.classList.remove('is-dragging')});
composer.addEventListener('drop',function(event){composer.classList.remove('is-dragging');var files=event.dataTransfer&&event.dataTransfer.files;if(files&&files.length&&stageFiles(files))event.preventDefault()});
}
document.addEventListener('submit',function(event){
var form=event.target.closest('form');
if(!form||!form.hasAttribute('hx-post'))return;
var submitter=event.submitter;
var action=submitter&&submitter.getAttribute('formaction')||form.getAttribute('hx-post');
if(!ownPath(action))return;
event.preventDefault();
if(form===composer&&(stagingFiles||(uploadFile&&uploadFile.files&&uploadFile.files.length))){
if(!stagingFiles)stageSelectedFiles();
announce('Wait for the selected files to finish saving, then send again.');
return;
}
if(form===composer){if(sending)return;sending=true;if(draftTimer){window.clearTimeout(draftTimer);draftTimer=null}}
var activeMessage=document.activeElement&&document.activeElement.closest?document.activeElement.closest('.message'):null;
var restoreMessageID=activeMessage?activeMessage.getAttribute('data-message-id'):'';
var quiet=form.getAttribute('data-quiet')==='true';
var body=new FormData(form);
var unixInput=form.querySelector('[data-unix-seconds="true"]');
if(unixInput&&unixInput.value){var unixMillis=new Date(unixInput.value).getTime();if(!isNaN(unixMillis))body.set('value',String(Math.floor(unixMillis/1000)))}
var scheduleInput=form.querySelector('[data-schedule-at]');
if(scheduleInput&&action.indexOf('/app/message/schedule')===0&&scheduleInput.value){var scheduleMillis=new Date(scheduleInput.value).getTime();if(!isNaN(scheduleMillis))body.set('post_at',String(Math.floor(scheduleMillis/1000)))}
var sent=text?text.value:'';
var button=submitter||form.querySelector('button[type=submit]');
if(button)button.disabled=true;
var releaseButton=function(){if(button)button.disabled=false};
var release=function(){releaseButton();if(form===composer)sending=false};
clearError(form);
fetch(action,{method:'POST',body:body,headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){
if(!response.ok)return response.text().then(function(body){throw new Error(body)});
if(response.headers.get('X-SameOldChat-Draft-Cleanup')==='failed')announce('Your message was sent, but its old draft could not be cleared. Delete it from Drafts & sent.');
var redirect=response.headers.get('HX-Redirect');
if(redirect){if(form===composer&&text&&text.value===sent){text.value='';draftAttachments=[];syncDraftAttachments();persistDraft()}if(ownPath(redirect))window.location.assign(redirect);return null}
if(response.status===204)return '';
return response.text();
}).then(function(html){
releaseButton();
if(html===null)return null;
if(quiet){form.hidden=true;return null}
if(html===''){return refresh(true).then(function(){
if(restoreMessageID){var restored=Array.prototype.slice.call(document.querySelectorAll('.message')).find(function(item){return item.getAttribute('data-message-id')===restoreMessageID});focusMessage(restored)}
announce('The conversation was updated.');
})}
var newest=form===composer?form.getAttribute('data-newest'):'';
if(newest&&ownPath(newest)){
window.location.assign(newest);
return null;
}
var target=document.querySelector(form.getAttribute('hx-target'));
if(!target)throw new Error('The page could not be updated. Reload to see the message.');
target.insertAdjacentHTML('beforeend',html);
appliedHTML.delete(target);
localize(target);
if(form===composer&&text){if(text.value===sent){text.value='';draftAttachments=[];syncDraftAttachments();persistDraft()}text.focus()}else{form.reset()}
toBottom(target);
toBottom(document.getElementById('timeline'));
return refresh(true);
}).catch(function(error){showError(failure(error,form),form)}).then(release,release);
});
if(text&&composer){text.addEventListener('keydown',function(event){
var suggestions=slashSuggestions&&!slashSuggestions.hidden?slashSuggestions:mentionSuggestions&&!mentionSuggestions.hidden?mentionSuggestions:channelSuggestions&&!channelSuggestions.hidden?channelSuggestions:emojiSuggestions&&!emojiSuggestions.hidden?emojiSuggestions:null;
if(suggestions){
var options=suggestions===slashSuggestions?slashOptions():suggestions===mentionSuggestions?mentionOptions():suggestions===channelSuggestions?channelOptions():emojiOptions(emojiSuggestions);
var selected=options.findIndex(function(option){return option.getAttribute('aria-selected')==='true'});
if(event.key==='ArrowDown'||event.key==='ArrowUp'){
event.preventDefault();
if(options.length){if(selected<0)selected=0;else selected=event.key==='ArrowDown'?(selected+1)%options.length:(selected+options.length-1)%options.length;for(var optionIndex=0;optionIndex<options.length;optionIndex++){options[optionIndex].setAttribute('aria-selected',optionIndex===selected?'true':'false');options[optionIndex].removeAttribute('id')}options[selected].id='autocomplete-option-active';text.setAttribute('aria-activedescendant','autocomplete-option-active')}
return;
}
if(event.key==='Escape'){event.preventDefault();if(suggestions===slashSuggestions)hideSlashes();else if(suggestions===mentionSuggestions)hideMentions();else if(suggestions===channelSuggestions)hideChannels();else hideEmojiSuggestions();return}
if((event.key==='Enter'||event.key==='Tab')&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&options.length){event.preventDefault();if(suggestions===slashSuggestions)chooseSlash(options[selected<0?0:selected]);else if(suggestions===mentionSuggestions)chooseMention(options[selected<0?0:selected]);else if(suggestions===channelSuggestions)chooseChannel(options[selected<0?0:selected]);else chooseEmoji(options[selected<0?0:selected],true);return}
}
var formatKey=typeof event.key==='string'?event.key.toLowerCase():'';
if(primaryShortcut(event)&&!event.altKey&&(formatKey==='b'||formatKey==='i'||(event.shiftKey&&formatKey==='x'))){
event.preventDefault();
wrapComposerSelection(formatKey==='b'?'*':formatKey==='i'?'_':'~');
return;
}
if(event.key==='ArrowUp'&&!event.shiftKey&&!event.ctrlKey&&!event.metaKey&&!event.altKey&&text.value===''){
var target=composer.getAttribute('hx-target');
var region=target&&target.charAt(0)==='#'?document.querySelector(target):document.getElementById('timeline');
var items=messageItems(region);
if(items.length){event.preventDefault();focusMessage(items[items.length-1]);return}
}
if(event.key!=='Enter'||event.shiftKey||event.ctrlKey||event.metaKey||event.altKey||event.isComposing)return;
event.preventDefault();
if(sending)return;
if(typeof composer.requestSubmit==='function'){composer.requestSubmit();return}
composer.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));
})}
var keyboardHelpDialog=document.getElementById('keyboard-help');
var keyboardHelpQuery=document.getElementById('keyboard-help-query');
var keyboardHelpEmpty=document.getElementById('keyboard-help-empty');
function filterKeyboardHelp(){
if(!keyboardHelpDialog)return;
var term=keyboardHelpQuery?keyboardHelpQuery.value.trim().toLowerCase():'';
var shown=0;
Array.prototype.forEach.call(keyboardHelpDialog.querySelectorAll('[data-keyboard-row]'),function(row){
var match=!term||(row.getAttribute('data-keyboard-search')||'').indexOf(term)>=0;
row.hidden=!match;
if(match)shown++;
});
Array.prototype.forEach.call(keyboardHelpDialog.querySelectorAll('[data-keyboard-section]'),function(section){
section.hidden=!section.querySelector('[data-keyboard-row]:not([hidden])');
});
if(keyboardHelpEmpty)keyboardHelpEmpty.hidden=shown>0;
}
function openKeyboardHelp(){
if(!keyboardHelpDialog||typeof keyboardHelpDialog.showModal!=='function'||keyboardHelpDialog.open)return false;
Array.prototype.forEach.call(keyboardHelpDialog.querySelectorAll('[data-keyboard-apple]'),function(node){node.hidden=!applePlatform});
Array.prototype.forEach.call(keyboardHelpDialog.querySelectorAll('[data-keyboard-other]'),function(node){node.hidden=applePlatform});
if(keyboardHelpQuery)keyboardHelpQuery.value='';
filterKeyboardHelp();
keyboardHelpDialog.showModal();
if(keyboardHelpQuery)keyboardHelpQuery.focus();
return true;
}
if(keyboardHelpQuery)keyboardHelpQuery.addEventListener('input',filterKeyboardHelp);
var keyboardHelpClose=document.getElementById('keyboard-help-close');
if(keyboardHelpClose)keyboardHelpClose.addEventListener('click',function(){keyboardHelpDialog.close()});
var keyboardHelpOpen=document.getElementById('open-keyboard-help');
if(keyboardHelpOpen)keyboardHelpOpen.addEventListener('click',function(){openKeyboardHelp()});

function sectionLandmarks(){
return Array.prototype.slice.call(document.querySelectorAll('#workspace-sidebar,#workspace-search,#timeline,#thread-messages,#composer')).filter(function(node){return node&&node.offsetParent!==null||node===document.activeElement});
}
function moveSection(backwards){
var landmarks=sectionLandmarks();
if(landmarks.length<2)return false;
var active=document.activeElement;
var current=-1;
for(var index=0;index<landmarks.length;index++){if(landmarks[index]===active||landmarks[index].contains(active)){current=index;break}}
if(current<0)current=backwards?0:landmarks.length-1;
var next=backwards?(current+landmarks.length-1)%landmarks.length:(current+1)%landmarks.length;
var target=landmarks[next];
var focusable=target.matches('input,textarea,select,button,a[href]')?target:target.querySelector('input:not([type=hidden]),textarea,select,button,a[href],[tabindex="-1"]');
var destination=focusable||target;
if(!destination.hasAttribute('tabindex')&&!destination.matches('input,textarea,select,button,a[href]'))destination.setAttribute('tabindex','-1');
destination.focus();
announce('Moved to '+(target.getAttribute('aria-label')||target.id.replace(/[-_]/g,' ')));
return true;
}
function unreadLinks(){
return Array.prototype.slice.call(document.querySelectorAll('.side-section[aria-label="Channels"] .side-link,.side-section[aria-label="Direct messages"] .side-link')).filter(function(link){
return /unread messages/.test(link.getAttribute('aria-label')||'');
});
}
document.addEventListener('keydown',function(event){
var key=typeof event.key==='string'?event.key.toLowerCase():'';
if(event.key==='Escape'){
var modalClose=document.getElementById('modal-close-form');
if(modalClose){event.preventDefault();if(typeof modalClose.requestSubmit==='function')modalClose.requestSubmit();else modalClose.submit();return}
}
if(event.key==='Tab'&&nav&&nav.classList.contains('is-open')){
var focusable=navFocusables();
if(focusable.length){
var first=focusable[0];
var last=focusable[focusable.length-1];
var active=document.activeElement;
if(event.shiftKey&&(active===first||!nav.contains(active))){event.preventDefault();last.focus();return}
if(!event.shiftKey&&(active===last||!nav.contains(active))){event.preventDefault();first.focus();return}
}
}
if(event.key==='Escape'&&nav&&nav.classList.contains('is-open')){
event.preventDefault();
setNav(false,false);
if(navToggle)navToggle.focus();
return;
}
if(event.ctrlKey&&!event.metaKey&&!event.altKey&&key==='3'&&(applePlatform?!event.shiftKey:event.shiftKey)){
if(!activityLink)return;
var activityHref=activityLink.getAttribute('href');
if(!ownPath(activityHref))return;
event.preventDefault();
window.location.assign(activityHref);
return;
}
if(primaryShortcut(event)&&!event.shiftKey&&!event.altKey&&key==='g'){
if(!search)return;
event.preventDefault();
search.focus();
search.select();
return;
}
if(primaryShortcut(event)&&!event.shiftKey&&!event.altKey&&key==='f'){
if(!search)return;
event.preventDefault();
var channelInput=search.form?search.form.querySelector('input[name=channel]'):null;
var searchParams=new URLSearchParams({channel:channelInput?channelInput.value:'',scope:'channel',type:'messages'});
window.location.assign('/app/search?'+searchParams.toString());
return;
}
if(primaryShortcut(event)&&!event.shiftKey&&!event.altKey&&key==='k'){
if(openSwitcher()){event.preventDefault();return}
}
if(primaryShortcut(event)&&event.shiftKey&&!event.altKey&&key==='i'){
var detailsLink=document.querySelector('a[aria-label="Open conversation details"]');
if(detailsLink&&ownPath(detailsLink.getAttribute('href'))){event.preventDefault();window.location.assign(detailsLink.getAttribute('href'));return}
}
if(primaryShortcut(event)&&!event.shiftKey&&!event.altKey&&key==='/'){
if(openKeyboardHelp()){event.preventDefault();return}
}
if(primaryShortcut(event)&&event.shiftKey&&!event.altKey&&key==='k'){
var directsLink=document.querySelector('.side-link[aria-label="Direct messages"]');
if(directsLink&&ownPath(directsLink.getAttribute('href'))){event.preventDefault();window.location.assign(directsLink.getAttribute('href'));return}
}
if(primaryShortcut(event)&&event.shiftKey&&!event.altKey&&key==='a'){
var unreadsLink=document.querySelector('.side-link[aria-label="Unreads"]');
if(unreadsLink&&ownPath(unreadsLink.getAttribute('href'))){event.preventDefault();window.location.assign(unreadsLink.getAttribute('href'));return}
}
if(primaryShortcut(event)&&event.shiftKey&&!event.altKey&&key==='t'){
var threadsLink=document.querySelector('.side-link[aria-label="Threads"]');
if(threadsLink&&ownPath(threadsLink.getAttribute('href'))){event.preventDefault();window.location.assign(threadsLink.getAttribute('href'));return}
}
if(primaryShortcut(event)&&event.shiftKey&&!event.altKey&&key==='s'){
var laterLink=document.querySelector('.side-link[aria-keyshortcuts~="Meta+Shift+S"]');
if(laterLink&&ownPath(laterLink.getAttribute('href'))){event.preventDefault();window.location.assign(laterLink.getAttribute('href'));return}
}
if(primaryShortcut(event)&&!event.altKey&&event.key==='F6'){
if(moveSection(event.shiftKey)){event.preventDefault();return}
}
if(primaryShortcut(event)&&!event.shiftKey&&!event.altKey&&key==='u'){
var upload=document.querySelector('#composer input[type=file]');
if(upload){event.preventDefault();upload.click();return}
}
var target=event.target;
var editing=target&&(target.tagName==='INPUT'||target.tagName==='TEXTAREA'||target.isContentEditable);
var focusedMessage=target&&target.closest?target.closest('.message'):null;
if(focusedMessage&&target===focusedMessage&&!event.ctrlKey&&!event.metaKey&&!event.altKey){
var region=focusedMessage.closest('[data-fragment]');
var items=messageItems(region);
var position=items.indexOf(focusedMessage);
if(event.key==='ArrowUp'||event.key==='ArrowDown'||event.key==='Home'||event.key==='End'){
var next=position;
if(event.key==='ArrowUp')next=Math.max(0,position-1);
if(event.key==='ArrowDown')next=Math.min(items.length-1,position+1);
if(event.key==='Home')next=0;
if(event.key==='End')next=items.length-1;
if(next>=0){event.preventDefault();focusMessage(items[next]);return}
}
if(event.key==='ArrowRight'||key==='t'){
var reply=focusedMessage.querySelector('.message-actions a');
if(reply&&ownPath(reply.getAttribute('href'))){event.preventDefault();window.location.assign(reply.getAttribute('href'));return}
}
if(event.key==='ArrowLeft'){
var back=Array.prototype.slice.call(document.querySelectorAll('.channel-actions a')).find(function(link){return link.textContent.trim()==='Back to channel'});
if(back&&ownPath(back.getAttribute('href'))){event.preventDefault();window.location.assign(back.getAttribute('href'));return}
}
if(key==='f'&&openMessageDetails(focusedMessage,'Forward','select,input,button')){event.preventDefault();return}
if(key==='u'){
var unread=Array.prototype.slice.call(focusedMessage.querySelectorAll('.message-actions button')).find(function(button){return button.textContent.trim()==='Mark unread from here'});
if(unread){event.preventDefault();unread.click();return}
}
if(key==='e'&&openMessageDetails(focusedMessage,'Edit','textarea')){event.preventDefault();return}
if(event.key==='Delete'&&openMessageDetails(focusedMessage,'Delete','button[type=submit]')){event.preventDefault();return}
if(key==='r'){
var reactionButton=focusedMessage.querySelector('[data-open-emoji-picker][data-emoji-target="reaction"]');
if(reactionButton&&openEmojiPicker(reactionButton)){event.preventDefault();return}
}
if(key==='m'){
var reminderMenu=focusedMessage.querySelector('[data-reminder-menu]');
if(reminderMenu){event.preventDefault();revealDisclosure(reminderMenu);var reminderControl=reminderMenu.querySelector('button,input');if(reminderControl)reminderControl.focus();return}
}
if(key==='a'){
var save=focusedMessage.querySelector('[data-message-save] button[type=submit]');
if(save){event.preventDefault();var saveForm=save.closest('form');if(saveForm&&typeof saveForm.requestSubmit==='function')saveForm.requestSubmit(save);else if(saveForm)saveForm.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));return}
}
if(key==='p'){
var buttons=focusedMessage.querySelectorAll('.message-actions button[type=submit]');
var pin=Array.prototype.slice.call(buttons).find(function(button){var label=button.textContent.trim();return label==='Pin'||label==='Unpin'});
if(pin){event.preventDefault();var form=pin.closest('form');if(form&&typeof form.requestSubmit==='function')form.requestSubmit(pin);else if(form)form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));return}
}
}
if(event.key==='Escape'&&search&&document.activeElement===search&&text){
event.preventDefault();
text.focus();
return;
}
if(event.key==='Escape'&&(event.shiftKey||!editing)&&!document.querySelector('dialog[open]')&&!(nav&&nav.classList.contains('is-open'))){
var readForm=event.shiftKey?document.getElementById('mark-all-read'):document.getElementById('mark-read');
if(readForm){
event.preventDefault();
announce(event.shiftKey?'Marking every conversation read.':'Marking this conversation read.');
if(typeof readForm.requestSubmit==='function')readForm.requestSubmit();else readForm.submit();
return;
}
}
if(event.altKey&&event.shiftKey&&!event.ctrlKey&&!event.metaKey&&(event.key==='ArrowUp'||event.key==='ArrowDown')){
var unread=unreadLinks();
if(!unread.length){event.preventDefault();announce('No unread conversations.');return}
var here=unread.findIndex(function(link){return link.getAttribute('aria-current')==='page'});
var step=event.key==='ArrowDown'?1:-1;
var choice=here<0?(event.key==='ArrowDown'?0:unread.length-1):(here+step+unread.length)%unread.length;
var unreadHref=unread[choice].getAttribute('href');
if(!ownPath(unreadHref))return;
event.preventDefault();
window.location.assign(unreadHref);
return;
}
if(!event.altKey||event.shiftKey||event.ctrlKey||event.metaKey||(event.key!=='ArrowUp'&&event.key!=='ArrowDown'))return;
var links=Array.prototype.slice.call(document.querySelectorAll('.side-section[aria-label="Channels"] .side-link,.side-section[aria-label="Direct messages"] .side-link'));
if(links.length<2)return;
var current=links.findIndex(function(link){return link.getAttribute('aria-current')==='page'});
if(current<0)current=0;
var next=event.key==='ArrowDown'?(current+1)%links.length:(current+links.length-1)%links.length;
var href=links[next].getAttribute('href');
if(!ownPath(href))return;
event.preventDefault();
window.location.assign(href);
});
if(navToggle)navToggle.addEventListener('click',function(){setNav(!nav.classList.contains('is-open'),true)});
if(navScrim)navScrim.addEventListener('click',function(){setNav(false,false);if(navToggle)navToggle.focus()});
if(narrow){if(typeof narrow.addEventListener==='function')narrow.addEventListener('change',function(){setNav(false,false)});setNav(false,false)}
if(window.EventSource){
var cursor='';
try{cursor=sessionStorage.getItem('sameoldchat-last-event')||''}catch(error){cursor=''}
var stream=new EventSource('/events'+(cursor?'?last_event_id='+encodeURIComponent(cursor):''));
var deliver=function(event){
if(event.lastEventId){try{sessionStorage.setItem('sameoldchat-last-event',event.lastEventId)}catch(error){}}
try{document.dispatchEvent(new CustomEvent('sameoldchat:event',{detail:{type:event.type,data:event.data}}))}catch(error){}
if(event.type.indexOf('view.')===0){window.location.reload();return}
var live=regions(false);
if(!live.length){announce('New activity is available in this conversation.');return}
scheduleRefresh();
};
stream.onopen=function(){if(streamState){streamState='';announce('Live updates resumed.')}};
stream.onerror=function(){
if(stream.readyState===EventSource.CLOSED){streamState='closed';announce('Live updates stopped. Reload the page to reconnect.');return}
streamState='connecting';
announce('Reconnecting to live updates…');
};
topics.forEach(function(topic){stream.addEventListener(topic,deliver)});
}
var typingRegion=document.getElementById('typing');
if(typingRegion&&window.fetch&&typingRegion.getAttribute('data-channel')){
var typingTiming=` + typingTimingLiteral() + `;
var typingURL=typingRegion.getAttribute('data-typing')||'';
var typingChannel=typingRegion.getAttribute('data-channel')||'';
var typingCsrf=document.querySelector('#composer input[name=_csrf]');
var typingSent=0;
var typingClear=null;
var typingPending=null;
var renderTyping=function(){
if(!ownPath(typingURL))return;
fetch(typingURL,{headers:{'HX-Request':'true'},credentials:'same-origin'}).then(function(response){if(!response.ok)throw new Error('typing');return response.text()}).then(function(html){
typingRegion.innerHTML=html;
window.clearTimeout(typingClear);
if(typingRegion.textContent.trim())typingClear=window.setTimeout(renderTyping,typingTiming.ttl);
}).catch(function(){});
};
var scheduleTyping=function(){
if(typingPending)return;
typingPending=window.setTimeout(function(){typingPending=null;renderTyping()},200);
};
if(typeof stream!=='undefined'&&stream)stream.addEventListener('typing',function(event){
var frame=null;
try{frame=JSON.parse(event.data)}catch(error){return}
if(!frame||frame.channel!==typingChannel)return;
scheduleTyping();
});
if(text&&typingCsrf)text.addEventListener('input',function(){
var now=Date.now();
if(!text.value||now-typingSent<typingTiming.interval||!ownPath(typingURL))return;
typingSent=now;
var typingBody=new URLSearchParams();
typingBody.set('_csrf',typingCsrf.value);
fetch(typingURL,{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded'},body:typingBody.toString()}).catch(function(){});
});
}
var activityCsrf=document.querySelector('#composer input[name=_csrf],form input[name=_csrf]');
if(activityCsrf&&window.fetch){
var lastBeat=0;
var beat=function(){
var now=Date.now();
if(document.hidden||now-lastBeat<120000)return;
lastBeat=now;
var body=new URLSearchParams();
body.set('_csrf',activityCsrf.value);
fetch('/app/active',{method:'POST',credentials:'same-origin',headers:{'content-type':'application/x-www-form-urlencoded'},body:body.toString()}).catch(function(){});
};
beat();
['pointerdown','keydown','visibilitychange'].forEach(function(name){document.addEventListener(name,beat,{passive:true})});
window.setInterval(beat,300000);
}
var markRead=document.getElementById('mark-read');
if(markRead)submitQuietly(markRead);
var arrivedAt=null;
try{arrivedAt=window.location.hash?document.getElementById(decodeURIComponent(window.location.hash.slice(1))):null}catch(error){arrivedAt=null}
if(arrivedAt&&arrivedAt.classList&&arrivedAt.classList.contains('message')){
arrivedAt.classList.add('is-arrival');
focusMessage(arrivedAt);
announce('Opened the message you selected.');
var clearArrival=function(){arrivedAt.classList.remove('is-arrival');['pointerdown','keydown','wheel'].forEach(function(name){document.removeEventListener(name,clearArrival)})};
['pointerdown','keydown','wheel'].forEach(function(name){document.addEventListener(name,clearArrival,{once:false,passive:true})});
}else{
toBottom(document.getElementById('timeline'));
}
var activeModal=document.querySelector('[aria-modal="true"]');
if(activeModal){var modalFocus=activeModal.querySelector('input:not([type=hidden]),textarea,select,button');if(modalFocus)modalFocus.focus()}else if(arrivedAt&&arrivedAt.classList&&arrivedAt.classList.contains('message')){}else if(text)text.focus();
})();</script>`

func liveEventTopicsLiteral() string {
	quoted := make([]string, 0, len(liveEventTopics))
	for _, topic := range liveEventTopics {
		if strings.ContainsAny(topic, `'"\<>`) {
			panic("live event topic must be a plain identifier: " + topic)
		}
		quoted = append(quoted, "'"+topic+"'")
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (h Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /signed-out", h.signedOut)
	if h.Login != nil {
		h.Login.Register(mux)
		mux.HandleFunc("GET /auth/validation", h.validation)
		mux.HandleFunc("GET /me", h.me)
		// Identity-provider administration, and only that: every route here
		// reads or writes a provider, a provider-backed user, or an invitation
		// addressed to one. Without a provider there is nothing for them to
		// administer, so they are the routes that legitimately depend on one.
		mux.HandleFunc("GET /app/admin/auth", h.authAdminPage)
		// Deliberately reachable signed-out: the person it is for has no
		// account yet. See internal/web/invite.go for why it carries no secret.
		mux.HandleFunc("GET /app/invite/{inviteRequestID}", h.invitationPage)
		mux.HandleFunc("GET /api/admin.auth.methods.list", h.authMethodsList)
		mux.HandleFunc("POST /api/admin.auth.methods.set", h.authMethodSet)
		mux.HandleFunc("POST /api/admin.auth.users.invite", h.authUserInvite)
		mux.HandleFunc("POST /api/admin.auth.users.create", h.authUserCreate)
		mux.HandleFunc("GET /api/admin.auth.users.list", h.authUsersList)
		mux.HandleFunc("POST /api/admin.auth.users.set", h.authUserSet)
	}
	// Workspace administration is not identity-provider administration. These
	// govern retention, discoverability, default channels, analytics, the audit
	// log, and the invitation and app-request queues — none of which needs a
	// provider to exist, and all of which a deployment running on the static
	// development session still has to be able to govern. They were registered
	// only alongside a configured provider, so such a deployment had no
	// administration at all: the pages answered 404, not 403.
	mux.HandleFunc("GET /app/admin/audit", h.auditPage)
	mux.HandleFunc("GET /app/admin/settings", h.workspaceSettingsPage)
	mux.HandleFunc("GET /app/admin/analytics", h.analyticsPage)
	mux.HandleFunc("POST /app/admin/settings/identity", h.workspaceIdentitySet)
	mux.HandleFunc("POST /app/admin/settings/discoverability", h.workspaceDiscoverabilitySet)
	mux.HandleFunc("POST /app/admin/settings/disconnect", h.workspaceDisconnectTeam)
	mux.HandleFunc("POST /app/admin/settings/retention", h.workspaceRetentionSet)
	mux.HandleFunc("POST /app/admin/settings/default-channels", h.workspaceDefaultChannelsSet)
	mux.HandleFunc("POST /app/admin/invites/approve", h.authInviteRequestDecision(true))
	mux.HandleFunc("POST /app/admin/invites/deny", h.authInviteRequestDecision(false))
	mux.HandleFunc("POST /app/admin/apps/approve", h.authAppDecision(true))
	mux.HandleFunc("POST /app/admin/apps/restrict", h.authAppDecision(false))
	mux.HandleFunc("GET /app", h.index)
	mux.HandleFunc("GET /archives/{channelID}/{timestamp}", h.archivePermalink)
	mux.HandleFunc("POST /app/message/forward", h.forwardMessage)
	mux.HandleFunc("POST /app/files/delete", h.deleteFile)
	mux.HandleFunc("POST /app/files/describe", h.describeFile)
	mux.HandleFunc("GET /app/remote-files", h.remoteFiles)
	mux.HandleFunc("POST /app/workspace/switch", h.switchWorkspace)
	mux.HandleFunc("POST /app/conversation/retention", h.conversationRetentionSet)
	mux.HandleFunc("POST /app/conversation/retention/remove", h.conversationRetentionRemove)
	mux.HandleFunc("POST /app/connect/invite", h.connectInvite)
	mux.HandleFunc("POST /app/connect/approve", h.connectApprove)
	mux.HandleFunc("POST /app/connect/deny", h.connectDeny)
	mux.HandleFunc("GET /app/huddle", h.huddleFragment)
	mux.HandleFunc("POST /app/huddle/start", h.huddleMutation("started", h.startHuddle, "Huddle started"))
	mux.HandleFunc("POST /app/huddle/join", h.huddleMutation("joined", h.joinHuddle, "You joined the huddle"))
	mux.HandleFunc("POST /app/huddle/leave", h.huddleMutation("left", h.leaveHuddle, "You left the huddle"))
	mux.HandleFunc("POST /app/huddle/end", h.huddleMutation("ended", h.endHuddle, "Huddle ended"))
	mux.HandleFunc("POST /app/huddle/invite", h.huddleInvite)
	mux.HandleFunc("POST /app/huddle/signal", h.huddleSignal)
	mux.HandleFunc("POST /app/remote-files/share", h.shareRemoteFile)
	mux.HandleFunc("POST /app/remote-files/remove", h.removeRemoteFile)
	mux.HandleFunc("POST /app/read/unread", h.markUnreadFromHere)
	mux.HandleFunc("GET /oauth/authorize", h.oauthAuthorize)
	mux.HandleFunc("POST /oauth/authorize", h.oauthAuthorize)
	mux.HandleFunc("GET /oauth/v2/authorize", h.oauthAuthorize)
	mux.HandleFunc("POST /oauth/v2/authorize", h.oauthAuthorize)
	mux.HandleFunc("GET /app/timeline", h.timeline)
	mux.HandleFunc("POST /app/read", h.markRead)
	mux.HandleFunc("POST /app/read/all", h.markAllRead)
	mux.HandleFunc("POST /app/active", h.recordActivity)
	mux.HandleFunc("POST /app/typing", h.recordTyping)
	mux.HandleFunc("GET /app/typing", h.typingFragment)
	mux.HandleFunc("GET /app/search", h.search)
	mux.HandleFunc("GET /app/search/suggestions", h.searchSuggestions)
	mux.HandleFunc("GET /app/emoji/options", h.emojiOptions)
	mux.HandleFunc("GET /app/activity", h.activity)
	mux.HandleFunc("POST /app/activity/mutate", h.mutateActivity)
	mux.HandleFunc("POST /app/activity/preferences", h.setActivityPreferences)
	mux.HandleFunc("POST /app/activity/views", h.createActivitySavedView)
	mux.HandleFunc("POST /app/activity/views/delete", h.deleteActivitySavedView)
	mux.HandleFunc("POST /app/activity/read", h.acknowledgeActivityReminders)
	mux.HandleFunc("GET /app/notifications", h.notifications)
	mux.HandleFunc("POST /app/notifications/preferences", h.setWorkspaceNotifications)
	mux.HandleFunc("POST /app/notifications/dnd", h.setNotificationSnooze)
	mux.HandleFunc("POST /app/notifications/schedule", h.setNotificationSchedule)
	mux.HandleFunc("POST /app/notifications/vips", h.setNotificationVIP)
	mux.HandleFunc("GET /app/later", h.later)
	mux.HandleFunc("GET /app/threads", h.threadsPage)
	mux.HandleFunc("GET /app/unreads", h.unreadsPage)
	mux.HandleFunc("GET /app/drafts", h.draftsAndSent)
	mux.HandleFunc("GET /app/scheduled", h.scheduledMessages)
	mux.HandleFunc("GET /app/dms", h.directMessages)
	mux.HandleFunc("GET /app/members", h.members)
	mux.HandleFunc("GET /app/canvases", h.canvases)
	mux.HandleFunc("POST /app/canvases/create", h.createCanvas)
	mux.HandleFunc("GET /app/canvases/{canvasID}", h.canvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/update", h.updateCanvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/delete", h.deleteCanvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/restore", h.restoreCanvas)
	mux.HandleFunc("GET /app/channel-canvas", h.channelCanvas)
	mux.HandleFunc("POST /app/channel-canvas/create", h.createChannelCanvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/share", h.shareCanvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/share/revoke", h.revokeCanvasShare)
	mux.HandleFunc("POST /app/canvases/{canvasID}/comments", h.commentOnCanvas)
	mux.HandleFunc("POST /app/canvases/{canvasID}/comments/{commentID}/delete", h.deleteCanvasComment)
	mux.HandleFunc("GET /app/lists", h.lists)
	mux.HandleFunc("POST /app/lists/create", h.createList)
	mux.HandleFunc("GET /app/lists/{listID}", h.list)
	mux.HandleFunc("POST /app/lists/{listID}/columns", h.addListColumn)
	mux.HandleFunc("POST /app/lists/{listID}/columns/remove", h.removeListColumn)
	mux.HandleFunc("POST /app/lists/{listID}/share", h.shareList)
	mux.HandleFunc("POST /app/lists/{listID}/share/revoke", h.revokeListShare)
	mux.HandleFunc("POST /app/lists/{listID}/items/create", h.createListItem)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/toggle", h.toggleListItem)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/delete", h.deleteListItem)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/assign", h.assignListItem)
	mux.HandleFunc("GET /app/lists/{listID}/items/{itemID}", h.listItemPage)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/comments", h.commentOnListItem)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/comments/{commentID}/delete", h.deleteListItemComment)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/files", h.attachFileToListItem)
	mux.HandleFunc("POST /app/lists/{listID}/items/{itemID}/files/{fileID}/detach", h.detachFileFromListItem)
	mux.HandleFunc("GET /app/workflows", h.workflows)
	mux.HandleFunc("POST /app/workflows/{workflowID}/stop", h.stopWorkflow)
	mux.HandleFunc("POST /app/workflows/create", h.createWorkflow)
	mux.HandleFunc("GET /app/workflows/{workflowID}", h.workflow)
	mux.HandleFunc("POST /app/workflows/{workflowID}/update", h.updateWorkflow)
	mux.HandleFunc("POST /app/workflows/{workflowID}/copy", h.duplicateWorkflow)
	mux.HandleFunc("POST /app/workflows/{workflowID}/delete", h.deleteWorkflow)
	mux.HandleFunc("POST /app/workflows/{workflowID}/managers", h.setWorkflowManagers)
	mux.HandleFunc("POST /app/workflows/{workflowID}/permissions", h.setWorkflowPermission)
	mux.HandleFunc("GET /app/workflows/export/runs/{workflowID}", h.exportWorkflowRuns)
	mux.HandleFunc("GET /app/workflows/export/form-responses/{workflowID}", h.exportWorkflowFormResponses)
	mux.HandleFunc("POST /app/workflows/{workflowID}/triggers", h.createWorkflowTrigger)
	mux.HandleFunc("POST /app/workflows/{workflowID}/triggers/{triggerID}", h.updateWorkflowTrigger)
	mux.HandleFunc("POST /app/workflows/{workflowID}/triggers/{triggerID}/run", h.runWorkflow)
	mux.HandleFunc("GET /app/workflows/runs/{runID}", h.workflowRun)
	mux.HandleFunc("POST /app/workflows/runs/submit/{runID}", h.submitWorkflowForm)
	mux.HandleFunc("POST /app/workflows/runs/click/{runID}", h.completeWorkflowButton)
	mux.HandleFunc("GET /app/apps", h.workspaceApps)
	mux.HandleFunc("GET /app/apps/{appID}", h.appHome)
	mux.HandleFunc("POST /app/apps/{appID}/action", h.appHomeAction)
	mux.HandleFunc("GET /app/developer/apps", h.developerApps)
	mux.HandleFunc("POST /app/developer/apps", h.reloadDeveloperApps)
	mux.HandleFunc("POST /app/developer/apps/create", h.createDeveloperApp)
	mux.HandleFunc("POST /app/developer/apps/update", h.updateDeveloperApp)
	mux.HandleFunc("POST /app/developer/apps/delete", h.deleteDeveloperApp)
	mux.HandleFunc("POST /app/developer/apps/configuration-token", h.issueDeveloperConfigurationToken)
	mux.HandleFunc("POST /app/developer/apps/app-token", h.issueDeveloperAppToken)
	mux.HandleFunc("POST /app/developer/apps/app-token/revoke", h.revokeDeveloperAppTokens)
	mux.HandleFunc("POST /app/developer/apps/app-token/revoke-one", h.revokeDeveloperAppToken)
	mux.HandleFunc("GET /app/developer/apps/datastore", h.developerDatastore)
	mux.HandleFunc("POST /app/developer/apps/datastore/put", h.putDeveloperDatastoreItem)
	mux.HandleFunc("POST /app/developer/apps/datastore/delete", h.deleteDeveloperDatastoreItem)
	mux.HandleFunc("GET /app/developer/apps/delivery", h.developerAppDelivery)
	mux.HandleFunc("POST /app/profile", h.setProfile)
	mux.HandleFunc("POST /app/presence", h.setPresence)
	mux.HandleFunc("POST /app/status/schedule", h.scheduleStatus)
	mux.HandleFunc("POST /app/status/scheduled/update", h.updateScheduledStatus)
	mux.HandleFunc("POST /app/status/scheduled/delete", h.deleteScheduledStatus)
	mux.HandleFunc("POST /app/join", h.joinConversation)
	mux.HandleFunc("POST /app/message", h.postMessage)
	mux.HandleFunc("POST /app/draft", h.saveDraft)
	mux.HandleFunc("POST /app/draft/delete", h.deleteDraft)
	mux.HandleFunc("POST /app/message/schedule", h.scheduleMessage)
	mux.HandleFunc("POST /app/message/schedule/update", h.updateScheduledMessage)
	mux.HandleFunc("POST /app/message/schedule/send-now", h.sendScheduledMessageNow)
	mux.HandleFunc("POST /app/message/schedule/cancel", h.cancelScheduledMessage)
	mux.HandleFunc("POST /app/file/stage", h.stageDraftFiles)
	mux.HandleFunc("POST /app/file", h.uploadFile)
	mux.HandleFunc("GET /app/files/{fileID}", h.downloadFile)
	mux.HandleFunc("POST /app/interaction", h.appInteraction)
	mux.HandleFunc("POST /app/shortcut", h.appShortcut)
	mux.HandleFunc("POST /app/view/submit", h.viewSubmit)
	mux.HandleFunc("POST /app/view/action", h.viewAction)
	mux.HandleFunc("POST /app/view/close", h.viewClose)
	mux.HandleFunc("POST /app/options", h.appOptions)
	mux.HandleFunc("POST /app-response/{token}", h.appResponse)
	mux.HandleFunc("POST /app/message/update", h.updateMessage)
	mux.HandleFunc("POST /app/message/delete", h.deleteMessage)
	mux.HandleFunc("POST /app/conversation/create", h.createConversation)
	mux.HandleFunc("POST /app/conversation/open", h.openConversation)
	mux.HandleFunc("POST /app/conversation/add-people", h.addPeopleToDirectConversation)
	mux.HandleFunc("POST /app/conversation/convert-to-private", h.convertGroupDirectToPrivate)
	mux.HandleFunc("POST /app/conversation/visibility", h.setConversationVisibility)
	mux.HandleFunc("POST /app/conversation/invite", h.inviteConversationMember)
	mux.HandleFunc("POST /app/conversation/rename", h.renameConversation)
	mux.HandleFunc("POST /app/conversation/topic", h.setConversationTopic)
	mux.HandleFunc("POST /app/conversation/purpose", h.setConversationPurpose)
	mux.HandleFunc("POST /app/conversation/notifications", h.setConversationNotifications)
	mux.HandleFunc("POST /app/thread/follow", h.setThreadFollow)
	mux.HandleFunc("POST /app/conversation/archive", h.setConversationArchived)
	mux.HandleFunc("POST /app/conversation/leave", h.leaveConversation)
	mux.HandleFunc("POST /app/reaction", h.addReaction)
	mux.HandleFunc("POST /app/reaction/remove", h.removeReaction)
	mux.HandleFunc("POST /app/pin", h.addPin)
	mux.HandleFunc("POST /app/pin/remove", h.removePin)
	mux.HandleFunc("POST /app/later/save", h.saveForLater)
	mux.HandleFunc("POST /app/later/state", h.setSavedItemState)
	mux.HandleFunc("POST /app/later/remove", h.removeSavedItem)
	mux.HandleFunc("POST /app/reminders/create", h.createLaterReminder)
	mux.HandleFunc("POST /app/reminders/update", h.updateLaterReminder)
	mux.HandleFunc("POST /app/reminders/complete", h.completeLaterReminder)
	mux.HandleFunc("POST /app/reminders/delete", h.deleteLaterReminder)
	mux.HandleFunc("POST /app/session/revoke", h.revokeSession)
	mux.HandleFunc("POST /logout", h.revokeSession)
}

// requireCSRF is the mutation precondition, and it answers a person rather than
// a log line. The three failures are distinguishable and only one of them means
// "reload": a token that no longer matches the session is a page that has been
// open too long, a foreign origin is an attack, and no session is a sign-in.
func (h Handler) requireCSRF(w http.ResponseWriter, r *http.Request) bool {
	return h.acceptCSRFResult(w, r, auth.ValidateCSRF(r))
}

func (h Handler) acceptCSRFResult(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, auth.ErrCSRFCrossSite):
		h.writeMutationError(w, r, http.StatusForbidden, "That request came from another site",
			"This request was not made from SameOldChat, so nothing was changed. Open the workspace and try again.")
	case errors.Is(err, auth.ErrNoToken):
		h.writeMutationError(w, r, http.StatusUnauthorized, "Sign in required",
			"Your session has ended, so nothing was changed. Sign in and try again.")
	default:
		h.writeMutationError(w, r, http.StatusForbidden, "This page has been open too long",
			"Reload the page and send it again. Nothing was changed.")
	}
	return false
}

// writeMutationError answers a refused mutation in the shape the caller can
// use. The enhanced client puts the sentence next to the control that failed;
// a browser without JavaScript has navigated to this response, so it gets the
// styled page with a way back instead of a bare line of text on a white page.
func (h Handler) writeMutationError(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, message, status)
		return
	}
	h.writePageError(w, status, heading, message)
}

// ---------------------------------------------------------------------------
// Workspace page
// ---------------------------------------------------------------------------

// composerState carries a rejected submission back into the page so a failed
// post keeps the text the user typed and says what went wrong.
type composerState struct {
	Draft          string
	Attachments    []domain.DraftAttachment
	ScheduleAt     string
	Message        string
	Notice         string
	Status         int
	ModalErrors    map[string]string
	ModalSubmitted modalFormState
}

// historyReader is a principal that has been checked for
// auth.ScopeChannelsHistory. renderApp takes one instead of a bare principal,
// so the workspace render — the newest 50 messages, every author, the whole
// sidebar and a live CSRF token — cannot be reached from a handler that
// authenticated for a different scope. POST /app/message did exactly that: it
// gated on chat:write and then answered a rejected post with the full page, so
// a token that got 403 from GET /app read the conversation out of a 400 body.
type historyReader struct{ principal auth.Principal }

func requireHistoryReader(principal auth.Principal) (historyReader, error) {
	if !principal.HasScope(auth.ScopeChannelsHistory) {
		return historyReader{}, auth.ErrMissingScope
	}
	return historyReader{principal: principal}, nil
}

// archivePermalink resolves Slack's permalink shape,
// /archives/{channel}/p{ts-without-the-dot}, into the conversation window
// that contains the message and anchors the message itself.
//
// Every permalink this deployment hands out — chat.getPermalink, the copy
// link control, a link a member pastes to a colleague — used to point at a
// path no route served, on a host that exists nowhere. NAV-05 requires a
// permalink to open the containing conversation, load a window containing the
// target, and mark it; a removed or malformed target must be a distinct, safe
// outcome rather than a broken page.
// forwardMessage shares a message into another conversation, the way Slack's
// forward does: the destination receives the original's attribution and a
// link back to it, plus the forwarder's optional note.
//
// The original is quoted as an attachment rather than copied as text, so the
// forwarded message never pretends the forwarder wrote it, and the
// destination is validated as one this actor may post to — ACT-03 requires a
// forward not to disclose a destination the actor cannot reach.
// deleteFile removes a hosted file. Slack's delete is workspace-wide — the
// file leaves every message, search result and preview that referenced it —
// so the confirmation names that consequence rather than asking a bare
// "are you sure", which FILE-06 requires and the universal contract's
// destructive-action rule insists on.
func (h Handler) deleteFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the conversation and try again."); !ok {
		return
	}
	fileID := domain.FileID(strings.TrimSpace(r.URL.Query().Get("file")))
	if fileID == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The file was not deleted", "Reload the conversation and try again.")
		return
	}
	if err := h.Messages.DeleteFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID); err != nil {
		// The service is uploader-only and answers a missing file and someone
		// else's file identically, so this cannot be used to probe for files.
		h.writeMessageMutationError(w, r, err, "deleted")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	h.redirectMutation(w, r, appURL(channel, "", "", "", "")+"&notice="+url.QueryEscape("File deleted"))
}

// describeFile records what an image is, in words. It returns to the
// conversation rather than answering a fragment, because a description changes
// what every reader of that message hears and the timeline is where they see it.
func (h Handler) describeFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	form, ok := h.decodeMutation(w, r, "Reload the conversation and try again.")
	if !ok {
		return
	}
	fileID := domain.FileID(strings.TrimSpace(r.URL.Query().Get("file")))
	if fileID == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The description was not saved", "Reload the conversation and try again.")
		return
	}
	if err := h.Messages.SetFileDescription(r.Context(), principal.WorkspaceID, principal.UserID, fileID, form["description"]); err != nil {
		// Uploader-only, and a missing file and someone else's file answer
		// identically, so this cannot be used to probe for files.
		h.writeMessageMutationError(w, r, err, "described")
		return
	}
	notice := "Description saved"
	if strings.TrimSpace(form["description"]) == "" {
		notice = "Description removed"
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	h.redirectMutation(w, r, appURL(channel, "", "", "", "")+"&notice="+url.QueryEscape(notice))
}

func (h Handler) forwardMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	destination := domain.ConversationID(strings.TrimSpace(fields["destination"]))
	if channel == "" || timestamp == "" || destination == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "The message was not forwarded", "Choose a destination and try again.")
		return
	}
	original, lookupErr := h.Messages.MessageAt(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp)
	if lookupErr != nil {
		h.writeMessageMutationError(w, r, lookupErr, "forwarded")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	permalink := "/archives/" + url.PathEscape(string(channel)) + "/p" + strings.ReplaceAll(string(timestamp), ".", "")
	quoted := map[string]any{
		"author_name": names.name(original.AuthorID),
		"text":        original.Text,
		"footer":      "Forwarded from " + conversationDisplayName(r.Context(), h, principal, channel),
		"title":       "View the original message",
		"title_link":  permalink,
	}
	encoded, encodeErr := json.Marshal([]any{quoted})
	if encodeErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The message was not forwarded", "The original message could not be quoted.")
		return
	}
	if _, err := h.Messages.PostMessageAs(r.Context(), principal.WorkspaceID, principal.UserID, domain.MessagePostRequest{
		Conversation: destination,
		Text:         strings.TrimSpace(fields["comment"]),
		Attachments:  string(encoded),
	}); err != nil {
		h.writeMessageMutationError(w, r, err, "forwarded")
		return
	}
	h.redirectMutation(w, r, appURL(string(destination), "", "", "", "")+"&notice="+url.QueryEscape("Message forwarded"))
}

// conversationDisplayName names a conversation for a person, falling back to
// its identifier when the reader cannot read its metadata.
func conversationDisplayName(ctx context.Context, h Handler, principal auth.Principal, channel domain.ConversationID) string {
	conversation, err := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		return string(channel)
	}
	return conversationName(conversation)
}

// markUnreadFromHere moves the reader's cursor to just before the named
// message, which is how Slack's "mark unread from here" works: everything
// from that message onward becomes unread again, for this member only.
func (h Handler) markUnreadFromHere(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the conversation and try again."); !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	timestamp := strings.TrimSpace(r.URL.Query().Get("ts"))
	instant, parseErr := domain.ParseMessageTimestamp(domain.MessageTimestamp(timestamp))
	if channel == "" || parseErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The conversation was not marked unread", "Reload the conversation and try again.")
		return
	}
	// One microsecond before the message: the cursor names the last message
	// the reader HAS read, so the target itself must fall after it.
	before := domain.NewMessageTimestamp(instant.Add(-time.Microsecond))
	if _, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, channel, before); err != nil {
		h.writeMessageMutationError(w, r, err, "marked unread")
		return
	}
	h.redirectMutation(w, r, appURL(string(channel), "", "", "", "")+"&notice="+url.QueryEscape("Marked unread"))
}

func (h Handler) archivePermalink(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.PathValue("channelID")))
	raw := strings.TrimSpace(r.PathValue("timestamp"))
	digits, ok := strings.CutPrefix(raw, "p")
	if channel == "" || !ok || len(digits) < 7 {
		http.NotFound(w, r)
		return
	}
	// The permalink packs the timestamp without its dot; Slack's format is
	// six fractional digits, so the split is from the right.
	timestamp := domain.MessageTimestamp(digits[:len(digits)-6] + "." + digits[len(digits)-6:])
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		http.NotFound(w, r)
		return
	}
	message, err := h.Messages.MessageAt(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp)
	if err != nil {
		// A message that no longer exists, or one in a conversation this
		// member may not read, are the same answer: the link resolves to
		// nothing they can see, and it must not disclose which.
		//
		// The wording is this handler's own rather than writeStoreError's,
		// which says "That conversation is not available". For a permalink that
		// is usually false — the conversation is generally fine and the message
		// is not — and it sends a reader looking for the wrong problem.
		if errors.Is(err, store.ErrNotFound) {
			h.writePageError(w, http.StatusNotFound, "That message is not available", "It may have been deleted, or it may be in a conversation you cannot read. Pick a conversation from the sidebar to keep reading.")
			return
		}
		h.writeStoreError(w, err, "That message is no longer available.")
		return
	}
	// The window is built to CONTAIN the target: the cursor is one nanosecond
	// after the message, which is the same trick search results use to land
	// on a hit rather than just above it.
	before := ""
	boundary := message
	boundary.CreatedAt = boundary.CreatedAt.Add(time.Nanosecond)
	if cursor, cursorErr := domain.NewMessageCursor(boundary); cursorErr == nil {
		before = string(cursor)
	}
	http.Redirect(w, r, appURL(string(channel), string(message.ThreadTimestamp), before, messageAnchor(message.ID), ""), http.StatusSeeOther)
}

func (h Handler) index(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, h.signInTarget(r), http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, r, err)
		return
	}
	reader, err := requireHistoryReader(principal)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	h.renderApp(w, r, reader, composerState{Status: http.StatusOK})
}

func (h Handler) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		if r.Method == http.MethodGet && errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			target := "/login?return_to=" + url.QueryEscape(r.URL.RequestURI())
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, r, err)
		return
	}
	if r.Method == http.MethodPost {
		h.completeOAuthAuthorization(w, r, principal)
		return
	}
	fields, ok := singleValues(r.URL.Query())
	if !ok {
		h.writePageError(w, http.StatusBadRequest, "That authorization request is invalid", "One of its fields was supplied more than once.")
		return
	}
	if responseType := strings.TrimSpace(fields["response_type"]); responseType != "" && responseType != "code" {
		h.writePageError(w, http.StatusBadRequest, "That authorization request is invalid", "This server supports the authorization code flow.")
		return
	}
	if team := strings.TrimSpace(fields["team"]); team != "" && team != string(principal.WorkspaceID) {
		h.writePageError(w, http.StatusBadRequest, "That workspace is not available", "The requested app installation belongs to a different workspace.")
		return
	}
	request := oauthAuthorizationRequest(principal, fields)
	value, err := h.Messages.InspectOAuthAuthorization(r.Context(), request)
	if err != nil {
		h.writeOAuthAuthorizationError(w, err)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNoToken)
		return
	}
	h.writeHTML(w, oauthConsentTemplate, oauthConsentData{
		Action:              r.URL.Path,
		AppName:             value.AppName,
		BotScopes:           scopeConsentViews(value.BotScopes),
		UserScopes:          scopeConsentViews(value.UserScopes),
		CSRFToken:           auth.CSRFToken(sessionCookie.Value),
		ClientID:            value.ClientID,
		RedirectURI:         value.RedirectURI,
		State:               value.State,
		CodeChallenge:       value.CodeChallenge,
		CodeChallengeMethod: value.CodeChallengeMethod,
	}, http.StatusOK, "authorization consent is temporarily unavailable")
}

func (h Handler) completeOAuthAuthorization(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	fields, ok := h.decodeMutation(w, r, "The authorization request could not be read. Return to the app and try again.")
	if !ok {
		return
	}
	request := oauthAuthorizationRequest(principal, fields)
	if strings.TrimSpace(fields["decision"]) == "deny" {
		value, err := h.Messages.InspectOAuthAuthorization(r.Context(), request)
		if err != nil {
			h.writeOAuthAuthorizationError(w, err)
			return
		}
		redirectOAuthAuthorization(w, r, value.RedirectURI, value.State, "", "access_denied")
		return
	}
	if strings.TrimSpace(fields["decision"]) != "approve" {
		h.writePageError(w, http.StatusBadRequest, "Choose whether to allow this app", "The app was not authorized because no decision was selected.")
		return
	}
	value, err := h.Messages.AuthorizeOAuth(r.Context(), request)
	if err != nil {
		h.writeOAuthAuthorizationError(w, err)
		return
	}
	redirectOAuthAuthorization(w, r, value.RedirectURI, value.State, value.Code, "")
}

func oauthAuthorizationRequest(principal auth.Principal, fields map[string]string) domain.OAuthAuthorizationRequest {
	return domain.OAuthAuthorizationRequest{
		ClientID:            strings.TrimSpace(fields["client_id"]),
		WorkspaceID:         principal.WorkspaceID,
		UserID:              principal.UserID,
		RedirectURI:         strings.TrimSpace(fields["redirect_uri"]),
		BotScopes:           splitOAuthScopes(fields["scope"]),
		UserScopes:          splitOAuthScopes(fields["user_scope"]),
		State:               strings.TrimSpace(fields["state"]),
		CodeChallenge:       strings.TrimSpace(fields["code_challenge"]),
		CodeChallengeMethod: strings.TrimSpace(fields["code_challenge_method"]),
	}
}

func splitOAuthScopes(raw string) []string {
	return domain.NormalizeScopes(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}))
}

func singleValues(values url.Values) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for name, entries := range values {
		if len(entries) != 1 {
			return nil, false
		}
		result[name] = entries[0]
	}
	return result, true
}

func (h Handler) writeOAuthAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidOAuthClient):
		h.writePageError(w, http.StatusBadRequest, "That app could not be found", "The client ID does not identify an active app.")
	case errors.Is(err, service.ErrInvalidOAuth), errors.Is(err, service.ErrInvalidAppManifest):
		h.writePageError(w, http.StatusBadRequest, "That authorization request is invalid", "The redirect address, requested permissions, or PKCE parameters do not match the app configuration.")
	case errors.Is(err, store.ErrNotFound):
		h.writePageError(w, http.StatusForbidden, "That app cannot be installed here", "Your account or workspace is not eligible for this installation.")
	default:
		h.writePageError(w, http.StatusServiceUnavailable, "Authorization is temporarily unavailable", "Nothing was installed. Return to the app and try again.")
	}
}

func redirectOAuthAuthorization(w http.ResponseWriter, r *http.Request, rawRedirect, state, code, failure string) {
	target, err := url.Parse(rawRedirect)
	if err != nil {
		http.Error(w, "authorization redirect is invalid", http.StatusBadRequest)
		return
	}
	query := target.Query()
	if code != "" {
		query.Set("code", code)
	}
	if failure != "" {
		query.Set("error", failure)
	}
	if state != "" {
		query.Set("state", state)
	}
	target.RawQuery = query.Encode()
	secureHeaders(w, workspaceContentSecurityPolicy)
	http.Redirect(w, r, target.String(), http.StatusFound)
}

// markRead advances the reader's unread cursor.
//
// This is a durable write and therefore a POST with a CSRF check. GET /app used
// to perform it inline, on a channel named in the query string, with no token
// at all: `<img src="/app?channel=C…">` or a link in any message silently wiped
// a victim's unread state, because SameSite=Lax sends the session cookie on a
// top-level cross-site navigation. A safe method must not write.
func (h Handler) markRead(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The read marker could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(fields["ts"]))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The unread marker could not be moved because the link does not identify a message in this conversation.")
		return
	}
	if _, err := h.Messages.MarkRead(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp); err != nil {
		// Unread bookkeeping is not worth an error page: the reader has read the
		// conversation either way.
		if errors.Is(err, store.ErrNotFound) {
			h.writeMutationError(w, r, http.StatusNotFound, "That conversation is not available", "The unread marker could not be moved because that conversation is no longer available.")
			return
		}
		if errors.Is(err, service.ErrNotInConversation) {
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this conversation", "The unread marker is only kept for conversations you have joined.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Unread counts are temporarily unavailable", "The unread marker could not be moved. Nothing else was changed.")
		return
	}
	h.completeMutation(w, r)
}

// markAllRead clears every unread conversation at once, which is Slack's
// Shift+Escape and the sidebar's "mark all as read".
//
// Like markRead this is a durable write, so it is a POST with a CSRF check —
// and more so: a forged GET here would wipe a member's entire unread state
// across the workspace, not one conversation's.
func (h Handler) markAllRead(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	cleared, err := h.Messages.MarkAllRead(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Unread counts are temporarily unavailable", "Nothing was marked read. Nothing else was changed.")
		return
	}
	// The count is the whole feedback: clearing every badge at once is
	// irreversible enough that "nothing happened" and "seventeen conversations
	// were cleared" must not look the same.
	notice := "Everything was already read"
	if cleared == 1 {
		notice = "Marked 1 conversation read"
	} else if cleared > 1 {
		notice = fmt.Sprintf("Marked %d conversations read", cleared)
	}
	h.redirectMutation(w, r, h.viewURL(r, "")+"&notice="+url.QueryEscape(notice))
}

// recordActivity is the automatic-presence heartbeat. It answers 204 and
// nothing else: the client sends it in the background and has no use for a
// body, and a page that re-rendered on every heartbeat would be worse than no
// automatic presence at all.
func (h Handler) recordActivity(w http.ResponseWriter, r *http.Request) {
	// The heartbeat answers no body when it succeeds and its caller discards
	// the response entirely, so its refusals answer no body either. They used
	// to render a whole workspace error page — through the shared page helpers,
	// on every beat, for as long as a signed-out tab stayed open — into a
	// response that nothing would ever read.
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		w.WriteHeader(jsonAuthStatus(err))
		return
	}
	if _, err := decodeFormFields(w, r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := auth.ValidateCSRF(r); err != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err := h.Messages.RecordActivity(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		// A missed heartbeat costs a member nothing but an early "away", so it
		// is not worth an error page.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signInTarget starts the exact provider the deployment can complete. A
// configured provider that the workspace has disabled would answer the bare
// "authorization method is disabled" page, so entry falls back to the provider
// list in that case.
func (h Handler) signInTarget(r *http.Request) string {
	if h.Login == nil || !h.Login.hasOpenIDConnectProvider() {
		return "/login"
	}
	if method, err := h.Messages.GetAuthMethod(r.Context(), h.Login.workspace, "oidc"); err == nil && !method.Enabled {
		return "/login"
	}
	return "/auth/oidc"
}

func (h Handler) renderApp(w http.ResponseWriter, r *http.Request, reader historyReader, state composerState) {
	principal := reader.principal
	channel := h.requestChannel(r)
	before := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("before")))
	threadTimestamp := strings.TrimSpace(r.URL.Query().Get("thread"))
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	csrfToken := auth.CSRFToken(sessionCookie.Value)

	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeStoreError(w, err, "This conversation is temporarily unavailable.")
		return
	}
	isMember, err := h.Messages.IsConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeStoreError(w, err, "Your membership in this conversation is temporarily unavailable.")
		return
	}
	workspace, err := h.Messages.WorkspaceInfo(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "The workspace identity is temporarily unavailable.")
		return
	}
	history, err := h.historyWindow(r.Context(), principal, channel, before)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			h.writePageError(w, http.StatusBadRequest, "That history link is not valid", "The link you followed does not point at a place in this conversation. Open the channel to read the latest messages.")
			return
		}
		h.writeStoreError(w, err, "The message store is temporarily unavailable.")
		return
	}
	names := h.newUserNames(r.Context(), principal)
	notices := make([]string, 0, 4)
	// Mutations redirect back here with ?notice=…, exactly as the canvas, list
	// and document pages do. This page never read it, so every action that
	// redirected here — marking unread, and now marking everything read —
	// completed in total silence: the member saw the page reload and had no
	// confirmation that anything had happened.
	//
	// The value is reflected, so it is bounded and escaped. Rendering is
	// html/template's job; the bound is this function's, because a redirect is
	// something any page can be sent to.
	if notice := strings.TrimSpace(r.URL.Query().Get("notice")); notice != "" {
		if len(notice) > 200 {
			notice = notice[:200]
		}
		notices = append(notices, notice)
	}
	timelineSummaries, timelineLastRead := h.timelineChrome(r.Context(), principal, conversation.ID, history.Messages, isMember)
	forwardDestinations := []conversationView(nil)
	if isMember && principal.HasScope(auth.ScopeChatWrite) {
		if options, optionsErr := h.visibleChannelOptions(r.Context(), principal); optionsErr == nil {
			forwardDestinations = options
		}
	}
	timeline, timelineNotice := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: csrfToken, Messages: history.Messages, Thread: threadTimestamp, Before: string(before), Member: isMember, Names: names, IncludeEphemeral: before == "" && threadTimestamp == "", ThreadSummaries: timelineSummaries, LastRead: timelineLastRead, ForwardDestinations: forwardDestinations})
	if timelineNotice != "" {
		notices = append(notices, timelineNotice)
	}

	var thread messageList
	if threadTimestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(domain.MessageTimestamp(threadTimestamp)); parseErr != nil {
			h.writePageError(w, http.StatusBadRequest, "That thread link is not valid", "The link you followed does not identify a message in this conversation.")
			return
		}
		replies, repliesErr := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp), domain.PageRequest{Limit: timelineWindow})
		if repliesErr != nil {
			h.writeStoreError(w, repliesErr, "The thread is temporarily unavailable.")
			return
		}
		var threadNotice string
		thread, threadNotice = h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: csrfToken, Messages: replies.Messages, Thread: threadTimestamp, Before: string(before), ThreadPane: true, Member: isMember, Names: names})
		if threadNotice != "" && timelineNotice == "" {
			notices = append(notices, threadNotice)
		}
	}

	conversations := h.sidebar(r.Context(), principal, channel, history.AtLatest, domain.Cursor(strings.TrimSpace(r.URL.Query().Get("conversations"))))
	if conversations.Notice != "" {
		notices = append(notices, conversations.Notice)
	}

	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your identity is temporarily unavailable.")
		return
	}
	username := displayName(current)
	workspaceName := strings.TrimSpace(workspace.Name)
	if workspaceName == "" {
		workspaceName = "SameOldChat"
	}
	channelPrefix := "#"
	channelName := conversationName(conversation)
	var channelStatusDisplay template.HTML
	var channelStatusText string
	if conversation.IsDirectOrGroup() {
		channelPrefix = ""
		// A one-to-one DM's header is a person: resolve that person once so the
		// title and their current status come from the same read. A group DM is
		// several people and keeps the joined-names title with no single status.
		if conversation.Kind == domain.ConversationTypeIM {
			if other, ok := h.otherDirectMember(r.Context(), principal, conversation.ID); ok {
				channelName = displayName(other)
				if strings.TrimSpace(other.Profile.StatusEmoji) != "" {
					emojiImages := map[string]string{}
					if customEmoji, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID); err == nil {
						emojiImages = customEmojiImages(customEmoji)
					}
					channelStatusDisplay = statusEmojiDisplay(other.Profile.StatusEmoji, emojiImages)
					channelStatusText = other.Profile.StatusText
				}
			} else if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
				channelName = participants
			}
		} else if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
			channelName = participants
		}
	}
	canJoin := !isMember && !conversation.Archived && conversation.Kind.OrPublic() == domain.ConversationTypePublic && principal.HasScope(auth.ScopeChannelsManage)
	canPost := isMember && !conversation.Archived && principal.HasScope(auth.ScopeChatWrite)
	var composerMembers []memberView
	var composerGroups []userGroupView
	var composerChannels []conversationView
	if canPost {
		memberPage, memberErr := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: memberWindow})
		if memberErr != nil {
			notices = append(notices, "Mention suggestions are temporarily unavailable.")
		} else {
			composerMembers = make([]memberView, 0, len(memberPage.Users))
			for _, user := range memberPage.Users {
				if user.Deleted {
					continue
				}
				name := displayName(user)
				composerMembers = append(composerMembers, memberView{ID: string(user.ID), Name: name, AuthorInitial: initial(name), IsSelf: user.ID == principal.UserID})
			}
			sort.Slice(composerMembers, func(left, right int) bool {
				return strings.ToLower(composerMembers[left].Name) < strings.ToLower(composerMembers[right].Name)
			})
			if memberPage.HasMore {
				notices = append(notices, "Mention suggestions show the first 100 conversation members.")
			}
		}
		groupCursor := domain.Cursor("")
		seenGroupCursors := make(map[domain.Cursor]struct{})
		for {
			if _, repeated := seenGroupCursors[groupCursor]; repeated {
				notices = append(notices, "User group suggestions stopped at an invalid page boundary.")
				composerGroups = nil
				break
			}
			seenGroupCursors[groupCursor] = struct{}{}
			groupPage, groupErr := h.Messages.ListUserGroups(r.Context(), principal.WorkspaceID, principal.UserID, false, domain.PageRequest{Limit: memberWindow, Cursor: groupCursor})
			if groupErr != nil {
				notices = append(notices, "User group suggestions are temporarily unavailable.")
				composerGroups = nil
				break
			}
			for _, group := range groupPage.Groups {
				if !group.Enabled || !group.DeletedAt.IsZero() {
					continue
				}
				composerGroups = append(composerGroups, userGroupView{
					ID: string(group.ID), Name: group.Name, Handle: group.Handle,
					Description: group.Description, MemberCount: len(group.Users),
				})
			}
			if !groupPage.HasMore || groupPage.NextCursor == "" {
				break
			}
			groupCursor = groupPage.NextCursor
		}
		sort.Slice(composerGroups, func(left, right int) bool {
			return strings.ToLower(composerGroups[left].Handle) < strings.ToLower(composerGroups[right].Handle)
		})
		composerChannels, err = h.visibleChannelOptions(r.Context(), principal)
		if err != nil {
			notices = append(notices, "Channel suggestions are temporarily unavailable.")
			composerChannels = nil
		}
	}
	var globalShortcuts []domain.AppShortcut
	var slashCommands []domain.AppShortcut
	if canPost {
		slashCommands = builtInSlashCommands()
	}
	if isMember && principal.HasScope(auth.ScopeChatWrite) && threadTimestamp == "" {
		globalShortcuts, err = h.Messages.ListAppShortcuts(r.Context(), principal.WorkspaceID, principal.UserID, "global")
		if err != nil {
			notices = append(notices, "App shortcuts are temporarily unavailable.")
		}
		appCommands, commandErr := h.Messages.ListAppShortcuts(r.Context(), principal.WorkspaceID, principal.UserID, "slash")
		if commandErr != nil {
			notices = append(notices, "App slash commands are temporarily unavailable.")
		} else {
			builtIns := make(map[string]struct{}, len(slashCommands))
			for _, command := range slashCommands {
				builtIns[command.Command] = struct{}{}
			}
			for _, command := range appCommands {
				if _, reserved := builtIns[command.Command]; !reserved {
					slashCommands = append(slashCommands, command)
				}
			}
			sort.Slice(slashCommands, func(left, right int) bool {
				return slashCommands[left].Command < slashCommands[right].Command
			})
		}
	}
	workspaceApps, appsErr := h.Messages.ListWorkspaceApps(r.Context(), principal.WorkspaceID, principal.UserID)
	if appsErr != nil {
		notices = append(notices, "Installed apps are temporarily unavailable.")
	}
	var modal *modalView
	currentModal, modalErr := h.Messages.CurrentModalView(r.Context(), principal.WorkspaceID, principal.UserID)
	if modalErr == nil {
		failures := make(map[string]string, len(currentModal.Errors)+len(state.ModalErrors))
		for blockID, message := range currentModal.Errors {
			failures[blockID] = message
		}
		for blockID, message := range state.ModalErrors {
			failures[blockID] = message
		}
		modal, modalErr = h.newModalView(r.Context(), principal, currentModal, failures, state.ModalSubmitted)
	}
	if modalErr != nil && !errors.Is(modalErr, store.ErrNotFound) {
		notices = append(notices, "An app modal is temporarily unavailable.")
	}
	if state.Notice != "" {
		notices = append(notices, state.Notice)
	}
	reminderUnread, reminderErr := h.hasUnacknowledgedReminder(r.Context(), principal)
	if reminderErr != nil {
		notices = append(notices, "Reminder notification state is temporarily unavailable.")
	}
	var details *conversationDetailsView
	if strings.TrimSpace(r.URL.Query().Get("details")) == "1" {
		details, err = h.newConversationDetails(r.Context(), principal, workspace, conversation, isMember)
		if err != nil {
			notices = append(notices, "Conversation details are temporarily unavailable.")
		}
	}
	if state.Draft == "" && len(state.Attachments) == 0 && isMember {
		draft, draftErr := h.Messages.Draft(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp))
		switch {
		case draftErr == nil:
			state.Draft = draft.Text
			state.Attachments = draft.Attachments
		case errors.Is(draftErr, store.ErrNotFound):
		default:
			notices = append(notices, "Your saved draft is temporarily unavailable.")
		}
	}
	if isMember {
		draftPage, draftErr := h.Messages.Drafts(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 1000, Descending: true})
		if draftErr != nil {
			notices = append(notices, "Draft indicators are temporarily unavailable.")
		} else {
			withDraft := make(map[domain.ConversationID]bool, len(draftPage.Items))
			for _, draft := range draftPage.Items {
				withDraft[draft.ConversationID] = true
			}
			for index := range conversations.Channels {
				conversations.Channels[index].HasDraft = withDraft[domain.ConversationID(conversations.Channels[index].ID)]
			}
			for index := range conversations.Directs {
				conversations.Directs[index].HasDraft = withDraft[domain.ConversationID(conversations.Directs[index].ID)]
			}
		}
	}
	// The header's member count. A DM's header is the person, so no count; a
	// failure to read one degrades to not showing it, because the header must
	// not take the page down for a decoration — but it is logged, not
	// swallowed, so a broken count is visible somewhere.
	memberCount := 0
	if !conversation.IsDirectOrGroup() || conversation.Kind == domain.ConversationTypeMPIM {
		if count, countErr := h.Messages.ConversationMemberCount(r.Context(), principal.WorkspaceID, principal.UserID, channel); countErr == nil {
			memberCount = count
		} else {
			log.Printf("web: the member count for %s could not be read: %v", channel, countErr)
		}
	}

	draftAttachments := newDraftAttachmentViews(state.Attachments)
	draftJSON, _ := json.Marshal(draftAttachments)

	data := pageData{
		Timeline:             timeline,
		Thread:               thread,
		ThreadTimestamp:      threadTimestamp,
		Channels:             conversations.Channels,
		Directs:              conversations.Directs,
		Channel:              string(channel),
		ChannelName:          channelName,
		ChannelPrefix:        channelPrefix,
		ChannelStatusDisplay: channelStatusDisplay,
		ChannelStatusText:    channelStatusText,
		ChannelMeta:          conversationMeta(conversation),
		MemberCount:          memberCount,
		WorkspaceName:        workspaceName,
		CSRFToken:            csrfToken,
		ShowProfile:          h.canShowIdentity(),
		ShowAdmin:            h.canShowWorkspaceAdmin(r.Context(), principal),
		ShowAuthAdmin:        h.Login != nil && h.canShowWorkspaceAdmin(r.Context(), principal),
		Keyboard:             keyboardHelp(),
		Assistant:            h.assistantThreadView(r.Context(), principal, channel, domain.MessageTimestamp(threadTimestamp)),
		ReminderUnread:       reminderUnread,
		CanvasURL:            channelCanvasURL(principal, conversation, isMember),
		IsMember:             isMember,
		CanPost:              canPost,
		CanSchedule:          principal.HasScope(auth.ScopeChatWrite),
		CanUpload:            isMember && !conversation.Archived && principal.HasScope(auth.ScopeChatWrite) && principal.HasScope(auth.ScopeFilesWrite),
		CanJoin:              canJoin,
		CanCreate:            principal.HasScope(auth.ScopeChannelsManage),
		Username:             username,
		UserInitial:          initial(username),
		AtLatest:             history.AtLatest,
		Notice:               strings.Join(notices, " "),
		Error:                state.Message,
		Draft:                state.Draft,
		DraftAttachments:     draftAttachments,
		DraftJSON:            string(draftJSON),
		ScheduleAt:           state.ScheduleAt,
		ComposeURL:           mutationURL("/app/message", string(channel), "", threadTimestamp, ""),
		DraftURL:             mutationURL("/app/draft", string(channel), "", threadTimestamp, ""),
		ScheduleURL:          mutationURL("/app/message/schedule", string(channel), "", threadTimestamp, ""),
		UploadURL:            mutationURL("/app/file", string(channel), "", threadTimestamp, ""),
		StageUploadURL:       mutationURL("/app/file/stage", string(channel), "", threadTimestamp, ""),
		TimelineURL:          fragmentURL(string(channel), "", string(before)),
		ThreadURL:            fragmentURL(string(channel), threadTimestamp, ""),
		GlobalShortcuts:      globalShortcuts,
		SlashCommands:        slashCommands,
		ComposerMembers:      composerMembers,
		ComposerGroups:       composerGroups,
		ComposerChannels:     composerChannels,
		Apps:                 workspaceApps,
		Modal:                modal,
		Details:              details,
	}
	if canJoin {
		data.JoinURL = mutationURL("/app/join", string(channel), "", threadTimestamp, "")
	}
	// The huddle bar is a live fragment, so the page renders the same partial
	// the refresh fetches: a first paint that disagreed with the first refresh
	// would flicker the control set for anyone reading it.
	data.Workspaces = h.workspaceChoices(r, principal)
	// A failure to read either leaves notifications off: the safe default for
	// something that interrupts a person is not to.
	if preferences, prefErr := h.Messages.WorkspaceNotificationPreferences(r.Context(), principal.WorkspaceID, principal.UserID); prefErr == nil {
		data.BrowserNotifications = preferences.BrowserNotifications
		// Outside the member's window is a fourth reason nothing arrives, and
		// it reaches the client through the same attribute the other reasons
		// do — one gate rather than a second one the client could forget.
		if !preferences.Schedule.AllowsAt(time.Now()) {
			data.NotificationsPaused = true
		}
	}
	if dnd, dndErr := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID); dndErr == nil && dnd.SnoozeUntil.After(time.Now()) {
		data.NotificationsPaused = true
	}
	data.HuddleURL = "/app/huddle?channel=" + url.QueryEscape(string(channel))
	data.Huddle = h.huddleFor(r.Context(), principal, conversation, data.CSRFToken, "", names)
	if isMember && threadTimestamp != "" {
		following, followErr := h.Messages.ThreadFollowed(
			r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp),
		)
		if followErr != nil {
			data.Notice = strings.TrimSpace(data.Notice + " Thread follow state is temporarily unavailable.")
		} else {
			data.ThreadFollowURL = mutationURL("/app/thread/follow", string(channel), "", threadTimestamp, "")
			data.FollowingThread = following
		}
	}
	if conversations.More != "" {
		data.MoreChannelsURL = appURL(string(channel), threadTimestamp, string(before), "", string(conversations.More))
	}
	// Marking the conversation read is a durable write, so the page carries a
	// form for it rather than performing it while rendering. The client submits
	// the form once the timeline has settled; a reader without JavaScript sees
	// the control and decides for themselves.
	if isMember && history.AtLatest && conversations.CurrentUnread > 0 && len(history.Messages) > 0 {
		last := history.Messages[len(history.Messages)-1]
		data.MarkReadURL = mutationURL("/app/read", string(channel), "", threadTimestamp, "")
		data.MarkReadTimestamp = string(domain.NewMessageTimestamp(last.CreatedAt))
	}
	if history.OlderCursor != "" {
		data.OlderURL = appURL(string(channel), threadTimestamp, string(history.OlderCursor), "", "")
	}
	if !history.AtLatest {
		data.LatestURL = appURL(string(channel), threadTimestamp, "", "", "")
	}
	status := state.Status
	if status == 0 {
		status = http.StatusOK
	}
	h.writeHTML(w, pageTemplate, data, status, "page rendering unavailable")
}

// timeline renders the message region on its own so live updates and mutations
// re-render exactly what the server owns, instead of reloading the page and
// discarding the composer draft, the scroll position and the focus ring.
func (h Handler) timeline(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
	if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := h.requestChannel(r)
	threadTimestamp := strings.TrimSpace(r.URL.Query().Get("thread"))
	before := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("before")))
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeFragmentError(w, err, "the conversation is temporarily unavailable")
		return
	}
	isMember, err := h.Messages.IsConversationMember(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeFragmentError(w, err, "your conversation membership is temporarily unavailable")
		return
	}
	var messages []domain.Message
	if threadTimestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(domain.MessageTimestamp(threadTimestamp)); parseErr != nil {
			secureHeaders(w, workspaceContentSecurityPolicy)
			http.Error(w, "that thread link is not valid", http.StatusBadRequest)
			return
		}
		replies, repliesErr := h.Messages.Replies(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(threadTimestamp), domain.PageRequest{Limit: timelineWindow})
		if repliesErr != nil {
			h.writeFragmentError(w, repliesErr, "the thread is temporarily unavailable")
			return
		}
		messages = replies.Messages
	} else {
		history, historyErr := h.historyWindow(r.Context(), principal, channel, before)
		if historyErr != nil {
			if errors.Is(historyErr, domain.ErrInvalidCursor) {
				secureHeaders(w, workspaceContentSecurityPolicy)
				http.Error(w, "that history link is not valid", http.StatusBadRequest)
				return
			}
			h.writeFragmentError(w, historyErr, "the message store is temporarily unavailable")
			return
		}
		messages = history.Messages
	}
	fragmentSummaries, fragmentLastRead := h.timelineChrome(r.Context(), principal, conversation.ID, messages, isMember)
	list, _ := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: auth.CSRFToken(sessionCookie.Value), Messages: messages, Thread: threadTimestamp, Before: string(before), ThreadPane: threadTimestamp != "", Member: isMember, Names: h.newUserNames(r.Context(), principal), IncludeEphemeral: before == "" && threadTimestamp == "", ThreadSummaries: fragmentSummaries, LastRead: fragmentLastRead})
	h.writeFragment(w, list)
}

// historyWindow is the newest window of a conversation, or the window ending at
// end when the reader has navigated into older history.
//
// History is read newest-first in one bounded keyset page, then reversed for
// chronological rendering. The cursor names the oldest displayed row, so the
// next descending request starts strictly before it and adjacent windows cannot
// overlap or skip a message.
type historyView struct {
	Messages    []domain.Message
	OlderCursor domain.Cursor
	AtLatest    bool
}

func (h Handler) historyWindow(ctx context.Context, principal auth.Principal, channel domain.ConversationID, end domain.Cursor) (historyView, error) {
	page, err := h.Messages.History(ctx, principal.WorkspaceID, principal.UserID, channel, domain.PageRequest{
		Limit:      timelineWindow,
		Cursor:     end,
		Descending: true,
	})
	if err != nil {
		return historyView{}, err
	}
	newestFirst := make([]domain.Message, 0, timelineWindow)
	for _, message := range page.Messages {
		if len(newestFirst) == timelineWindow {
			break
		}
		newestFirst = append(newestFirst, message)
	}
	view := historyView{AtLatest: end == ""}
	if page.HasMore && len(newestFirst) > 0 {
		cursor, cursorErr := domain.NewMessageCursor(newestFirst[len(newestFirst)-1])
		if cursorErr != nil {
			return historyView{}, cursorErr
		}
		view.OlderCursor = cursor
	}
	slices.Reverse(newestFirst)
	view.Messages = newestFirst
	return view, nil
}

// messageListRequest is everything a message region needs to render: the
// conversation it belongs to, the messages in it, and the view state that its
// controls have to return to.
type messageListRequest struct {
	Conversation        domain.Conversation
	CSRFToken           string
	Messages            []domain.Message
	Thread              string
	Before              string
	ThreadPane          bool
	Member              bool
	Names               *userNames
	IncludeEphemeral    bool
	ForwardDestinations []conversationView
	// ThreadSummaries carries what each rendered parent's thread has
	// accumulated, read once for the whole window rather than per message.
	ThreadSummaries map[domain.MessageTimestamp]domain.ThreadSummary
	// LastRead is the reader's cursor, used to place the unread divider. The
	// zero value means the reader has read nothing in this conversation.
	LastRead domain.MessageTimestamp
}

// timelineChrome reads the two per-window facts the chrome needs: what each
// rendered parent's thread has accumulated, and where this reader has read
// to. Both are one call for the whole window — the per-message alternative is
// one read per rendered row — and both degrade to nothing rather than failing
// the conversation: a missing summary hides a reply count, a missing cursor
// hides the unread divider, and neither is worth a blank page.
func (h Handler) timelineChrome(ctx context.Context, principal auth.Principal, conversation domain.ConversationID, messages []domain.Message, member bool) (map[domain.MessageTimestamp]domain.ThreadSummary, domain.MessageTimestamp) {
	roots := make([]domain.MessageTimestamp, 0, len(messages))
	for _, message := range messages {
		if message.Deleted || message.ThreadTimestamp != "" {
			continue
		}
		roots = append(roots, domain.NewMessageTimestamp(message.CreatedAt))
	}
	summaries, err := h.Messages.ThreadSummaries(ctx, principal.WorkspaceID, principal.UserID, conversation, roots)
	if err != nil {
		summaries = nil
	}
	if !member {
		return summaries, ""
	}
	cursor, err := h.Messages.ReadCursor(ctx, principal.WorkspaceID, principal.UserID, conversation)
	if err != nil {
		return summaries, ""
	}
	return summaries, cursor.LastRead
}

// decodeMessageBroadcast reports whether a threaded reply was also sent to
// the channel. The flag lives in the durable stream state, which the API
// projects as subtype thread_broadcast; MSG-01 requires the two to be
// distinguishable in the client too.
func decodeMessageBroadcast(streamState string) bool {
	if strings.TrimSpace(streamState) == "" {
		return false
	}
	var state domain.MessageStreamState
	if json.Unmarshal([]byte(streamState), &state) != nil {
		return false
	}
	return state.ReplyBroadcast
}

// threadReplySummary is the sentence under a parent message: how many replies
// and who wrote them, in Slack's phrasing.
func threadReplySummary(summary domain.ThreadSummary, names *userNames) string {
	replies := "1 reply"
	if summary.ReplyCount != 1 {
		replies = strconv.Itoa(summary.ReplyCount) + " replies"
	}
	if len(summary.Participants) == 0 {
		return replies
	}
	people := make([]string, 0, len(summary.Participants))
	for _, participant := range summary.Participants {
		people = append(people, names.name(participant))
	}
	if len(people) > 3 {
		people = append(people[:3], "and others")
	}
	return replies + " from " + strings.Join(people, ", ")
}

// markDaysAndFirstUnread annotates the rendered window with the two
// boundaries a reader navigates by: where each day starts, and where their
// unread messages begin.
//
// It runs after the ephemeral merge, because that merge re-sorts and can
// truncate the slice — a divider computed before it would sit on the wrong
// message. It is server-side for the same reason every other flag here is:
// the fragment refresh re-renders this partial, and a boundary computed in
// the browser would vanish on the next live update.
func markDaysAndFirstUnread(views []messageView, messages []domain.Message, lastRead domain.MessageTimestamp) {
	previousDay := ""
	unreadMarked := lastRead == ""
	for index := range views {
		created := messages[index].CreatedAt.UTC()
		day := created.Format("2006-01-02")
		if day != previousDay {
			views[index].DaySeparator = created.Format("Monday, 2 January 2006")
			views[index].DaySeparatorMachine = day
			previousDay = day
		}
		if !unreadMarked && !views[index].Ephemeral {
			if domain.NewMessageTimestamp(messages[index].CreatedAt) > lastRead {
				views[index].FirstUnread = true
				unreadMarked = true
			}
		}
	}
}

// newMessageList builds the single type the message partial renders. It also
// reports a user-facing notice when an adjacent read (reactions, pins) is
// degraded, instead of failing the whole conversation view.
// resolveCallBlocks fills in Slack's call blocks from the calls the workspace
// actually knows about. A message carries only `{"type":"call","call_id":…}`,
// so before this the block rendered as nothing at all: an app could register a
// call through calls.add, post a message referring to it, and the member would
// see an empty message.
//
// Each distinct identifier is read once per page. There is normally none, and a
// page carrying more than a handful is not a shape Slack produces; the bound
// keeps a crafted message from turning one render into an unbounded number of
// reads.
// assistantThreadView reads what an assistant app has set on the open thread.
// A thread nothing has touched is the overwhelmingly common case and answers an
// empty view rather than an error, so a missing row is not a failed page.
func (h Handler) assistantThreadView(ctx context.Context, principal auth.Principal, conversation domain.ConversationID, thread domain.MessageTimestamp) assistantThreadView {
	if thread == "" {
		return assistantThreadView{}
	}
	value, err := h.Messages.AssistantThread(ctx, principal.WorkspaceID, principal.UserID, conversation, thread)
	if err != nil {
		return assistantThreadView{}
	}
	view := assistantThreadView{Present: true, Title: value.Title, Status: value.Status, PromptsTitle: value.PromptsTitle}
	for _, prompt := range value.Prompts {
		view.Prompts = append(view.Prompts, assistantPromptView{Title: prompt.Title, Message: prompt.Message})
	}
	return view
}

func (h Handler) resolveCallBlocks(ctx context.Context, principal auth.Principal, messages []messageView) {
	const maximumCallsPerPage = 20
	resolved := map[string]*callBlockView{}
	for index := range messages {
		for block := range messages[index].Blocks {
			id := messages[index].Blocks[block].CallID
			if id == "" {
				continue
			}
			view, seen := resolved[id]
			if !seen {
				if len(resolved) >= maximumCallsPerPage {
					continue
				}
				view = &callBlockView{Unavailable: true}
				if call, err := h.Messages.GetCall(ctx, principal.WorkspaceID, principal.UserID, domain.CallID(id)); err == nil {
					view = &callBlockView{
						Title:        call.Title,
						JoinURL:      call.JoinURL,
						Participants: h.callParticipantNames(ctx, principal, call.Participants),
						Active:       call.Active(),
					}
				}
				resolved[id] = view
			}
			messages[index].Blocks[block].Call = view
		}
	}
}

func (h Handler) callParticipantNames(ctx context.Context, principal auth.Principal, participants []domain.UserID) []string {
	if len(participants) == 0 {
		return nil
	}
	names := h.newUserNames(ctx, principal)
	values := make([]string, 0, len(participants))
	for _, participant := range participants {
		values = append(values, names.name(participant))
	}
	return values
}

func (h Handler) newMessageList(ctx context.Context, principal auth.Principal, request messageListRequest) (messageList, string) {
	conversation := request.Conversation
	csrfToken := request.CSRFToken
	messages := request.Messages
	ephemeralIDs := make(map[domain.MessageID]struct{})
	if request.IncludeEphemeral {
		values, err := h.Messages.ListEphemeralMessages(ctx, principal.WorkspaceID, principal.UserID, request.Conversation.ID, timelineWindow)
		if err != nil {
			// The durable channel history remains useful when the private
			// response projection is temporarily unavailable; report that
			// degradation beside it instead of blanking the conversation.
			values = nil
		}
		for _, value := range values {
			ephemeralIDs[value.ID] = struct{}{}
			messages = append(messages, domain.Message{
				ID: value.ID, WorkspaceID: value.WorkspaceID, Conversation: value.Conversation,
				AuthorID: value.AuthorID, AppID: value.AppID, Text: value.Text, Blocks: value.Blocks,
				Attachments: value.Attachments, CreatedAt: value.CreatedAt,
			})
		}
		sort.Slice(messages, func(left, right int) bool {
			if messages[left].CreatedAt.Equal(messages[right].CreatedAt) {
				return messages[left].ID < messages[right].ID
			}
			return messages[left].CreatedAt.Before(messages[right].CreatedAt)
		})
		if len(messages) > timelineWindow {
			messages = messages[len(messages)-timelineWindow:]
		}
	}
	threadTimestamp := request.Thread
	before := request.Before
	names := request.Names
	// The thread pane repeats messages that the timeline already shows, so
	// every identifier it generates is namespaced: one document cannot carry the
	// same id twice. The prefix used to be applied to the anchor only, while
	// messageView.ID — which builds `id="reaction-<id>"` and its `for=` label —
	// kept the bare message identifier, so opening a thread produced duplicate
	// ids and a label that pointed at the wrong control.
	anchorPrefix := ""
	if request.ThreadPane {
		anchorPrefix = "thread-"
	}
	channel := string(conversation.ID)
	list := messageList{
		ForwardDestinations: request.ForwardDestinations,
		ChannelName:         conversationName(conversation),
		CSRFToken:           csrfToken,
		IsMember:            request.Member,
		CanReact:            request.Member && principal.HasScope(auth.ScopeReactionsWrite),
		CanPin:              request.Member && principal.HasScope(auth.ScopePinsWrite),
		CanReply:            request.Member && principal.HasScope(auth.ScopeChatWrite),
		Messages:            make([]messageView, 0, len(messages)),
	}
	notice := ""
	emojiImages := map[string]string{}
	if customEmoji, err := h.Messages.Emojis(ctx, principal.WorkspaceID, principal.UserID); err != nil {
		notice = "Custom emoji are temporarily unavailable."
	} else {
		emojiImages = customEmojiImages(customEmoji)
	}
	pinned := map[domain.MessageID]struct{}{}
	if principal.HasScope(auth.ScopePinsRead) || principal.HasScope(auth.ScopePinsWrite) {
		pins, _, _, err := h.Messages.Pins(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: pinWindow})
		if err != nil {
			notice = "Pinned messages are temporarily unavailable."
		}
		for _, pin := range pins {
			pinned[pin.Message] = struct{}{}
		}
	}
	saved := make(map[domain.MessageID]domain.SavedItem)
	messageIDs := make([]domain.MessageID, 0, len(messages))
	for _, message := range messages {
		if message.Deleted {
			continue
		}
		if _, ephemeral := ephemeralIDs[message.ID]; !ephemeral {
			messageIDs = append(messageIDs, message.ID)
		}
	}
	if items, err := h.Messages.SavedItemsForMessages(ctx, principal.WorkspaceID, principal.UserID, messageIDs); err != nil {
		if notice == "" {
			notice = "Saved items are temporarily unavailable."
		}
	} else {
		for _, item := range items {
			saved[item.MessageID] = item
		}
	}
	readReactions := principal.HasScope(auth.ScopeReactionsRead) || principal.HasScope(auth.ScopeReactionsWrite)
	var messageShortcuts []domain.AppShortcut
	if request.Member {
		var err error
		messageShortcuts, err = h.Messages.ListAppShortcuts(ctx, principal.WorkspaceID, principal.UserID, "message")
		if err != nil && notice == "" {
			notice = "App message shortcuts are temporarily unavailable."
		}
	}
	actionCatalog := appActionOptionCatalog{}
	rendered := make([]domain.Message, 0, len(messages))
	for _, message := range messages {
		// Deletion is soft in the store and leaves the text in place, so every
		// region has to drop the row rather than trust the read to have done it.
		if message.Deleted {
			continue
		}
		_, ephemeral := ephemeralIDs[message.ID]
		timestamp := string(domain.NewMessageTimestamp(message.CreatedAt))
		author := names.name(message.AuthorID)
		presentation := decodeMessageStreamPresentation(message.StreamState)
		if presentation.Username != "" {
			author = presentation.Username
		}
		displayMessage := message
		displayMessage.Text = resolveSlackReferences(message.Text, names)
		displayMessage.Blocks = resolveSlackReferenceJSON(message.Blocks, names)
		displayMessage.Attachments = resolveSlackReferenceJSON(message.Attachments, names)
		content := newRichMessageContent(displayMessage, emojiImages)
		actionCatalog.enrich(ctx, h, principal, content.Blocks)
		ownsMessage := !ephemeral && request.Member && message.AuthorID == principal.UserID && principal.HasScope(auth.ScopeChatWrite)
		view := messageView{
			ID:            anchorPrefix + string(message.ID),
			MessageID:     string(message.ID),
			Anchor:        anchorPrefix + messageAnchor(message.ID),
			AuthorName:    author,
			AuthorInitial: initial(author),
			AvatarURL:     presentation.IconURL,
			AvatarEmoji:   presentation.IconEmoji,
			IsApp:         message.AppID != "",
			Text:          message.Text,
			DisplayText:   content.Text,
			Blocks:        content.Blocks,
			Attachments:   content.Attachments,
			Unfurls:       content.Unfurls,
			MachineTime:   message.CreatedAt.UTC().Format(time.RFC3339Nano),
			DisplayTime:   formatTime(message.CreatedAt),
			Channel:       channel,
			ChannelName:   list.ChannelName,
			ReplyURL:      appURL(channel, timestamp, before, "", ""),
			ReactionURL:   mutationURL("/app/reaction", channel, timestamp, threadTimestamp, before),
			UnreactURL:    mutationURL("/app/reaction/remove", channel, timestamp, threadTimestamp, before),
			PinURL:        mutationURL("/app/pin", channel, timestamp, threadTimestamp, before),
			UnpinURL:      mutationURL("/app/pin/remove", channel, timestamp, threadTimestamp, before),
			SaveURL:       mutationURL("/app/later/save", channel, timestamp, threadTimestamp, before),
			RemindURL:     mutationURL("/app/reminders/create", channel, timestamp, threadTimestamp, before),
			UpdateURL:     mutationURL("/app/message/update", channel, timestamp, threadTimestamp, before),
			DeleteURL:     mutationURL("/app/message/delete", channel, timestamp, threadTimestamp, before),
			// The first-party editor currently writes plain text. Offering it on
			// an API-authored rich message would erase its blocks and
			// attachments on save, so rich messages retain deletion but are
			// edited through the Slack API that can round-trip their structure.
			CanEdit:     ownsMessage && !hasStructuredMessageContent(message.Blocks) && !hasStructuredMessageContent(message.Attachments),
			CanDelete:   ownsMessage,
			AppID:       string(message.AppID),
			CanInteract: request.Member && message.AppID != "",
			Streaming:   messageStreamActive(message.StreamState),
			Ephemeral:   ephemeral,
			Subtype:     string(message.Subtype),
			System:      message.Subtype.System(),
			Broadcast:   decodeMessageBroadcast(message.StreamState),
		}
		// A human posting as themselves carries their current status beside their
		// name; an app message or one wearing a custom username is not a person.
		if message.AppID == "" && presentation.Username == "" && message.AuthorID != "" {
			if emoji := names.statusEmoji(message.AuthorID); emoji != "" {
				view.AuthorStatus = renderReactionEmoji(emoji, emojiImages)
				view.AuthorStatusText = names.statusText(message.AuthorID)
			}
		}
		if !message.EditedAt.IsZero() {
			view.Edited = true
			view.EditedTime = formatTime(message.EditedAt)
			view.EditedMachineTime = message.EditedAt.UTC().Format(time.RFC3339Nano)
		}
		if !ephemeral {
			// Slack's permalink, and the two actions that need one.
			view.Permalink = "/archives/" + url.PathEscape(channel) + "/p" + strings.ReplaceAll(timestamp, ".", "")
			view.CopyLinkURL = view.Permalink
			view.ForwardURL = mutationURL("/app/message/forward", channel, timestamp, threadTimestamp, before)
			if request.Member {
				view.MarkUnreadURL = mutationURL("/app/read/unread", channel, timestamp, threadTimestamp, before)
			}
		}
		if summary, ok := request.ThreadSummaries[domain.MessageTimestamp(timestamp)]; ok && summary.ReplyCount > 0 {
			view.ReplyCount = summary.ReplyCount
			view.ReplySummary = threadReplySummary(summary, names)
			if !summary.LastReplyAt.IsZero() {
				view.LastReplyTime = formatTime(summary.LastReplyAt)
			}
		}
		if item, ok := saved[message.ID]; ok {
			view.Saved = true
			view.SavedItemID = string(item.ID)
			view.UnsaveURL = mutationURL("/app/later/remove", channel, timestamp, threadTimestamp, before) + "&id=" + url.QueryEscape(string(item.ID))
		}
		for _, file := range message.Files {
			title := strings.TrimSpace(file.Title)
			if title == "" {
				title = file.Name
			}
			fileItem := fileView{
				ID: string(file.ID), Name: file.Name, Title: title, MIMEType: file.MIMEType,
				Size: formatFileSize(file.Size), Deleted: file.Deleted,
				DownloadURL:    "/app/files/" + url.PathEscape(string(file.ID)),
				IsImage:        file.IsImage() && !file.Deleted,
				Description:    file.Description,
				AccessibleName: file.AccessibleName(),
			}
			if !file.Deleted && file.Uploader == principal.UserID && principal.HasScope(auth.ScopeFilesWrite) {
				fileItem.DeleteURL = mutationURL("/app/files/delete", channel, timestamp, threadTimestamp, before) + "&file=" + url.QueryEscape(string(file.ID))
				if fileItem.IsImage {
					fileItem.DescribeURL = mutationURL("/app/files/describe", channel, timestamp, threadTimestamp, before) + "&file=" + url.QueryEscape(string(file.ID))
				}
			}
			view.Files = append(view.Files, fileItem)
		}
		if !ephemeral {
			view.Shortcuts = messageShortcuts
		}
		if _, ok := pinned[message.ID]; ok {
			view.Pinned = true
		}
		if readReactions && !ephemeral {
			reactions, _, _, err := h.Messages.Reactions(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.MessageTimestamp(timestamp), domain.PageRequest{Limit: reactionWindow})
			if err != nil && notice == "" {
				notice = "Reactions are temporarily unavailable."
			}
			view.Reactions = summarizeReactions(reactions, principal.UserID, emojiImages)
		}
		list.Messages = append(list.Messages, view)
		rendered = append(rendered, message)
	}
	// The day separators and the unread divider are placed over the messages
	// that were actually rendered: the loop above drops soft-deleted rows, so
	// the two slices only line up once both are built.
	markDaysAndFirstUnread(list.Messages, rendered, request.LastRead)
	h.resolveCallBlocks(ctx, principal, list.Messages)
	return list, notice
}

func formatFileSize(size int64) string {
	const unit = int64(1024)
	if size < unit {
		return strconv.FormatInt(size, 10) + " B"
	}
	value, suffix := float64(size), "KiB"
	for _, candidate := range []string{"KiB", "MiB", "GiB", "TiB"} {
		suffix = candidate
		value /= float64(unit)
		if value < float64(unit) || candidate == "TiB" {
			break
		}
	}
	return strconv.FormatFloat(value, 'f', 1, 64) + " " + suffix
}

type appActionOptionCatalog struct {
	usersLoaded         bool
	conversationsLoaded bool
	users               []messageActionOptionView
	conversations       []messageActionOptionView
	channels            []messageActionOptionView
}

func (c *appActionOptionCatalog) enrich(ctx context.Context, h Handler, principal auth.Principal, blocks []messageBlockView) {
	for blockIndex := range blocks {
		for actionIndex := range blocks[blockIndex].Actions {
			action := &blocks[blockIndex].Actions[actionIndex]
			switch action.Type {
			case "users_select", "multi_users_select":
				c.loadUsers(ctx, h, principal)
				action.Options = cloneActionOptions(c.users)
			case "conversations_select", "multi_conversations_select":
				c.loadConversations(ctx, h, principal)
				action.Options = cloneActionOptions(c.conversations)
			case "channels_select", "multi_channels_select":
				c.loadConversations(ctx, h, principal)
				action.Options = cloneActionOptions(c.channels)
			default:
				continue
			}
			markSelectedOptions(action.Options, action.InitialValues)
		}
	}
}

func (c *appActionOptionCatalog) loadUsers(ctx context.Context, h Handler, principal auth.Principal) {
	if c.usersLoaded {
		return
	}
	c.usersLoaded = true
	var cursor domain.Cursor
	for pageIndex := 0; pageIndex < 5; pageIndex++ {
		page, err := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 200, Cursor: cursor})
		if err != nil {
			return
		}
		for _, user := range page.Users {
			if !user.Deleted {
				c.users = append(c.users, messageActionOptionView{Text: displayName(user), Value: string(user.ID)})
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(c.users, func(left, right int) bool {
		return strings.ToLower(c.users[left].Text) < strings.ToLower(c.users[right].Text)
	})
}

func (c *appActionOptionCatalog) loadConversations(ctx context.Context, h Handler, principal auth.Principal) {
	if c.conversationsLoaded {
		return
	}
	c.conversationsLoaded = true
	var cursor domain.Cursor
	for pageIndex := 0; pageIndex < 5; pageIndex++ {
		page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
			Limit: 200, Cursor: cursor, ExcludeArchived: true,
		})
		if err != nil {
			return
		}
		for _, conversation := range page.Conversations {
			label := conversationName(conversation)
			if !conversation.IsDirectOrGroup() {
				label = "#" + label
			}
			option := messageActionOptionView{Text: label, Value: string(conversation.ID)}
			c.conversations = append(c.conversations, option)
			if conversation.Kind.OrPublic() == domain.ConversationTypePublic {
				c.channels = append(c.channels, option)
			}
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sort.Slice(c.conversations, func(left, right int) bool { return c.conversations[left].Text < c.conversations[right].Text })
	sort.Slice(c.channels, func(left, right int) bool { return c.channels[left].Text < c.channels[right].Text })
}

func cloneActionOptions(values []messageActionOptionView) []messageActionOptionView {
	return append([]messageActionOptionView(nil), values...)
}

func summarizeReactions(reactions []domain.Reaction, viewer domain.UserID, customEmoji ...map[string]string) []reactionView {
	if len(reactions) == 0 {
		return nil
	}
	order := make([]string, 0, len(reactions))
	counts := map[string]int{}
	mine := map[string]bool{}
	for _, reaction := range reactions {
		if _, seen := counts[reaction.Name]; !seen {
			order = append(order, reaction.Name)
		}
		counts[reaction.Name]++
		if reaction.UserID == viewer {
			mine[reaction.Name] = true
		}
	}
	sort.Strings(order)
	views := make([]reactionView, 0, len(order))
	var emojiImages map[string]string
	if len(customEmoji) > 0 {
		emojiImages = customEmoji[0]
	}
	for _, name := range order {
		views = append(views, reactionView{Name: name, Display: renderReactionEmoji(name, emojiImages), Count: counts[name], Mine: mine[name]})
	}
	return views
}

func renderReactionEmoji(name string, customEmoji map[string]string) template.HTML {
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), ":"))
	if imageURL := customEmoji[name]; imageURL != "" {
		return template.HTML(`<img class="custom-emoji" src="` + template.HTMLEscapeString(imageURL) + `" alt=":` + template.HTMLEscapeString(name) + `:" loading="lazy">`) // #nosec G203 -- URL was scheme-validated and every value is escaped.
	}
	if rendered, ok := slackemoji.ReactionUnicode(name); ok {
		return template.HTML(`<span class="standard-emoji" role="img" aria-label=":` + template.HTMLEscapeString(name) + `:">` + template.HTMLEscapeString(rendered) + `</span>`) // #nosec G203 -- every dynamic value is escaped.
	}
	return template.HTML(template.HTMLEscapeString(":" + name + ":")) // #nosec G203 -- the value is escaped immediately above.
}

// statusEmojiDisplay renders a member's status emoji shortcode as a glyph, or
// nothing at all when there is no status. A caller renders the result behind an
// {{if}}, so an empty status contributes no markup rather than an empty span.
func statusEmojiDisplay(shortcode string, customEmoji map[string]string) template.HTML {
	if strings.TrimSpace(shortcode) == "" {
		return ""
	}
	return renderReactionEmoji(shortcode, customEmoji)
}

// sidebarView is the conversation list plus the one fact the page outside it
// needs: whether the conversation being read still has unread messages, which
// decides whether the page offers to mark it read.
type sidebarView struct {
	Channels      []conversationView
	Directs       []conversationView
	More          domain.Cursor
	Notice        string
	CurrentUnread int
}

func (h Handler) sidebar(ctx context.Context, principal auth.Principal, channel domain.ConversationID, atLatest bool, cursor domain.Cursor) sidebarView {
	page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{Limit: conversationWindow, Cursor: cursor})
	if err != nil {
		return sidebarView{Notice: "The conversation list is temporarily out of date."}
	}
	view := sidebarView{
		Channels: make([]conversationView, 0, len(page.Conversations)),
		Directs:  make([]conversationView, 0, len(page.Conversations)),
	}
	resolved := 0
	for _, conversation := range page.Conversations {
		if conversation.ID == channel {
			view.CurrentUnread = conversation.UnreadCount
		}
		item := conversationView{ID: string(conversation.ID), Name: conversationName(conversation), Current: conversation.ID == channel, UnreadCount: conversation.UnreadCount, IsGroupDirect: conversation.Kind == domain.ConversationTypeMPIM}
		if item.Current && atLatest {
			// The page just rendered every message in this conversation; showing
			// its own unread badge tells the reader to read what they are reading.
			item.UnreadCount = 0
		}
		if !conversation.IsDirectOrGroup() {
			view.Channels = append(view.Channels, item)
			continue
		}
		if resolved < directNameWindow && (conversation.Kind != domain.ConversationTypeMPIM || conversation.Name == "" || conversation.Name == "direct") {
			resolved++
			if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
				item.Name = participants
			}
		}
		view.Directs = append(view.Directs, item)
	}
	if page.HasMore {
		view.More = page.NextCursor
	}
	return view
}

// participantNames names a direct conversation after the people in it: the
// stored name of every direct conversation is "direct", which is neither
// distinguishable nor useful in a sidebar.
func (h Handler) participantNames(ctx context.Context, principal auth.Principal, conversation domain.ConversationID) string {
	page, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation, domain.PageRequest{Limit: 10})
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(page.Users))
	for _, user := range page.Users {
		if user.ID == principal.UserID {
			continue
		}
		names = append(names, displayName(user))
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// otherDirectMember returns the member of a one-to-one DM who is not the reader,
// so the header can name whose conversation it is and show their current status.
// It reports false for a self-DM or when the members cannot be read.
func (h Handler) otherDirectMember(ctx context.Context, principal auth.Principal, conversation domain.ConversationID) (domain.User, bool) {
	page, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation, domain.PageRequest{Limit: 10})
	if err != nil {
		return domain.User{}, false
	}
	for _, user := range page.Users {
		if user.ID != principal.UserID {
			return user, true
		}
	}
	return domain.User{}, false
}

// newConversationDetails loads the channel drawer only when a reader opens it.
// The normal timeline therefore does not turn one page view into up to twenty
// directory queries. Slack caps an invite at 1,000 users; the same bound keeps
// this human-facing selector finite and makes an oversized workspace explicit
// instead of silently pretending that the first page is the full membership.
// workspaceName resolves an organization's name for display, falling back to
// its identifier.
//
// The fallback is the ordinary case for a connected organization, not an error:
// WorkspaceInfo requires the reader to be a member, and a host workspace's
// administrator is by definition not a member of the organization it invited.
// Showing the identifier is honest — it is the name this deployment can prove —
// where a blank row would hide a real participant. Resolving the name properly
// needs a read that says "the public identity of an organization I share a
// channel with", which is a cross-workspace directory this product does not
// have.
func (h Handler) workspaceName(ctx context.Context, principal auth.Principal, id domain.WorkspaceID) string {
	if id == "" {
		return ""
	}
	if workspace, err := h.Messages.WorkspaceInfo(ctx, id, principal.UserID); err == nil && strings.TrimSpace(workspace.Name) != "" {
		return workspace.Name
	}
	return string(id)
}

func (h Handler) newConversationDetails(ctx context.Context, principal auth.Principal, workspace domain.Workspace, conversation domain.Conversation, isMember bool) (*conversationDetailsView, error) {
	const maxDirectoryPages = 10

	// The member panel shows each member's presence and status the way the People
	// directory does; a status emoji is a shortcode resolved to a glyph here.
	emojiImages := map[string]string{}
	if customEmoji, err := h.Messages.Emojis(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		emojiImages = customEmojiImages(customEmoji)
	}
	membersByID := make(map[domain.UserID]struct{})
	members := make([]memberView, 0)
	cursor := domain.Cursor("")
	truncated := false
	for pageNumber := 0; pageNumber < maxDirectoryPages; pageNumber++ {
		page, err := h.Messages.ConversationMembers(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, user := range page.Users {
			if user.Deleted {
				continue
			}
			name := displayName(user)
			isSelf := user.ID == principal.UserID
			membersByID[user.ID] = struct{}{}
			members = append(members, memberView{ID: string(user.ID), Name: name, Profile: user.Profile, StatusDisplay: statusEmojiDisplay(user.Profile.StatusEmoji, emojiImages), Presence: webPresence(user.Presence, isSelf), AuthorInitial: initial(name), IsSelf: isSelf})
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pageNumber == maxDirectoryPages-1 {
			truncated = true
		}
	}

	canManage := isMember && principal.HasScope(auth.ScopeChannelsManage)
	isChannel := !conversation.IsDirectOrGroup()
	invitees := make([]memberView, 0)
	if canManage && !conversation.Archived && (isChannel || len(members) < 9) {
		cursor = ""
		for pageNumber := 0; pageNumber < maxDirectoryPages; pageNumber++ {
			page, err := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
			if err != nil {
				return nil, err
			}
			for _, user := range page.Users {
				if user.Deleted {
					continue
				}
				if _, exists := membersByID[user.ID]; exists {
					continue
				}
				name := displayName(user)
				invitees = append(invitees, memberView{ID: string(user.ID), Name: name, AuthorInitial: initial(name)})
			}
			if !page.HasMore || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			if pageNumber == maxDirectoryPages-1 {
				truncated = true
			}
		}
	}
	sort.Slice(members, func(left, right int) bool { return members[left].Name < members[right].Name })
	sort.Slice(invitees, func(left, right int) bool { return invitees[left].Name < invitees[right].Name })

	required := false
	for _, requiredChannel := range workspace.DefaultChannelIDs {
		if requiredChannel == conversation.ID {
			required = true
			break
		}
	}
	// The Slack Connect panel. A read failure leaves it empty rather than
	// failing the whole details view: the conversation is still readable, and
	// a modal that will not open because a sharing list could not be read is a
	// worse outcome than a missing section.
	connected := make([]connectOrganizationView, 0)
	outstanding := make([]connectInviteView, 0)
	connectDestinations := make([]conversationView, 0)
	if isChannel {
		if teams, _, _, teamsErr := h.Messages.AdminConversationTeams(ctx, principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: 100}); teamsErr == nil {
			for _, team := range teams {
				if team == conversation.WorkspaceID {
					continue
				}
				connected = append(connected, connectOrganizationView{ID: string(team), Name: h.workspaceName(ctx, principal, team)})
			}
		}
		canApprove := principal.HasScope(auth.ScopeConversationsConnectManage)
		// One instant for the whole list, so two invitations with the same
		// deadline cannot be rendered on opposite sides of it.
		now := time.Now().UTC()
		for _, status := range []domain.SharedInviteStatus{domain.SharedInvitePending, domain.SharedInviteApproved} {
			page, listErr := h.Messages.ListSharedInvites(ctx, principal.WorkspaceID, principal.UserID, status, domain.PageRequest{Limit: 25})
			if listErr != nil {
				continue
			}
			for _, invite := range page.Invites {
				if invite.ConversationID != conversation.ID {
					continue
				}
				view := connectInviteView{
					ID: string(invite.ID), Status: string(invite.Status),
					Target:     h.workspaceName(ctx, principal, invite.TargetWorkspaceID),
					Expired:    invite.Expired(now),
					CanApprove: canApprove && invite.Status == domain.SharedInvitePending && !invite.Expired(now),
					CanRevoke:  canApprove,
					ApproveURL: "/app/connect/approve?channel=" + url.QueryEscape(string(conversation.ID)),
					DenyURL:    "/app/connect/deny?channel=" + url.QueryEscape(string(conversation.ID)),
				}
				if invite.TargetEmail != "" && view.Target == "" {
					view.Target = invite.TargetEmail
				}
				if !invite.ExpiresAt.IsZero() {
					view.Expires = formatTime(invite.ExpiresAt)
				}
				outstanding = append(outstanding, view)
			}
		}
		if summaries, workspacesErr := h.Messages.UserWorkspaces(ctx, principal.WorkspaceID, principal.UserID); workspacesErr == nil {
			for _, summary := range summaries {
				if summary.Workspace.ID == principal.WorkspaceID {
					continue
				}
				connectDestinations = append(connectDestinations, conversationView{ID: string(summary.Workspace.ID), Name: summary.Workspace.Name})
			}
		}
	}
	// Retention is administrative, and a group direct message or the workspace
	// default channel cannot carry a custom limit at all — the same refusal the
	// API makes, so the control is absent rather than present and rejecting.
	retentionAllowed := isChannel && principal.HasScope(auth.ScopeAdminConversationsWrite)
	retentionDays, retentionCustom := 0, false
	retentionNote := ""
	if retentionAllowed {
		override, effective, retentionErr := h.Messages.ConversationRetention(ctx, principal.WorkspaceID, principal.UserID, conversation.ID)
		if retentionErr != nil {
			retentionAllowed = false
		} else {
			retentionDays, retentionCustom = effective, override.DurationDays > 0
			switch {
			case effective == 0:
				retentionNote = "Messages here are kept forever, following the workspace default."
			case retentionCustom:
				retentionNote = "This channel has its own limit."
			default:
				retentionNote = "This channel follows the workspace default."
			}
		}
	}
	name := conversationName(conversation)
	conversationType := "Channel"
	switch {
	case conversation.Kind == domain.ConversationTypeIM:
		conversationType = "Direct message"
		if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
			name = participants
		}
	case conversation.Kind == domain.ConversationTypeMPIM:
		conversationType = "Group direct message"
		if conversation.Name == "" || conversation.Name == "direct" {
			if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
				name = participants
			}
		}
	case conversation.Kind == domain.ConversationTypePrivate:
		conversationType = "Private channel"
	}
	archiveVerb := "Archive"
	if conversation.Archived {
		archiveVerb = "Unarchive"
	}
	notificationPreferences := domain.DefaultConversationNotificationPreferences(principal.WorkspaceID, principal.UserID, conversation.ID)
	canNotify := isMember && isChannel
	if canNotify {
		var err error
		notificationPreferences, err = h.Messages.ConversationNotificationPreferences(ctx, principal.WorkspaceID, principal.UserID, conversation.ID)
		if err != nil {
			return nil, err
		}
	}
	canConvert := false
	if canManage && conversation.Kind == domain.ConversationTypeMPIM {
		membership, err := h.Messages.WorkspaceMembership(ctx, principal.WorkspaceID, principal.UserID, principal.UserID)
		if err != nil {
			return nil, err
		}
		canConvert = !membership.UltraRestricted
	}
	// Changing a channel's visibility belongs to a workspace administrator, not
	// to whoever can manage the channel: it decides who in the whole workspace
	// may read what was said in it.
	canChangeVisibility := isChannel && !conversation.Archived && h.canShowWorkspaceAdmin(ctx, principal)
	return &conversationDetailsView{
		ID:                  string(conversation.ID),
		Name:                name,
		IsChannel:           isChannel,
		IsPrivate:           conversation.PrivateFlag(),
		CanChangeVisibility: canChangeVisibility,
		Topic:               conversation.Topic,
		Purpose:             conversation.Purpose,
		Type:                conversationType,
		Archived:            conversation.Archived,
		Members:             members,
		Invitees:            invitees,
		Truncated:           truncated,
		CloseURL:            appURL(string(conversation.ID), "", "", "", ""),
		CanEdit:             canManage && isChannel && !conversation.Archived,
		CanInvite:           canManage && isChannel && !conversation.Archived,
		CanAddPeople:        canManage && !isChannel && len(members) < 9 && len(invitees) > 0,
		CanConvert:          canConvert,
		CanLeave:            canManage && isChannel && !conversation.Archived && !required,
		CanClose:            canManage && !isChannel,
		CanArchive:          canManage && isChannel && !required,
		CanNotify:           canNotify,
		NotificationLevel:   string(notificationPreferences.Level),
		FollowEveryThread:   notificationPreferences.FollowEveryThread,
		ArchiveVerb:         archiveVerb,
		Connected:           connected,
		Outstanding:         outstanding,
		CanConnect:          canManage && isChannel && !conversation.Archived && principal.HasScope(auth.ScopeConversationsConnectWrite),
		CanSetRetention:     retentionAllowed,
		RetentionDays:       retentionDays,
		RetentionCustom:     retentionCustom,
		RetentionSummary:    retentionNote,
		ConnectURL:          "/app/connect/invite?channel=" + url.QueryEscape(string(conversation.ID)),
		ConnectHosts:        connectDestinations,
	}, nil
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func parseLaterState(value string) (domain.SavedItemState, bool) {
	switch domain.SavedItemState(strings.TrimSpace(value)) {
	case "", domain.SavedItemInProgress:
		return domain.SavedItemInProgress, true
	case domain.SavedItemArchived:
		return domain.SavedItemArchived, true
	case domain.SavedItemCompleted:
		return domain.SavedItemCompleted, true
	default:
		return "", false
	}
}

func laterActionURL(path string, id domain.SavedItemID, state, returnState domain.SavedItemState, channel string) string {
	query := url.Values{"id": {string(id)}, "channel": {channel}}
	if state != "" {
		query.Set("state", string(state))
	}
	if returnState != "" {
		query.Set("return_state", string(returnState))
	}
	return path + "?" + query.Encode()
}

func reminderActionURL(path string, id domain.LaterReminderID, state domain.SavedItemState, channel string) string {
	query := url.Values{"id": {string(id)}, "channel": {channel}, "return_state": {string(state)}}
	return path + "?" + query.Encode()
}

func (h Handler) later(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	state, ok := parseLaterState(r.URL.Query().Get("state"))
	if !ok {
		h.writePageError(w, http.StatusBadRequest, "That Later link is not valid", "Open Later from the workspace and choose a section.")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.SavedItems(r.Context(), principal.WorkspaceID, principal.UserID, state, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) || errors.Is(err, store.ErrInvalidArgument) {
			h.writePageError(w, http.StatusBadRequest, "That Later link is not valid", "Open Later from the workspace and try again.")
			return
		}
		h.writeStoreError(w, err, "Later is temporarily unavailable.")
		return
	}
	title := map[domain.SavedItemState]string{
		domain.SavedItemInProgress: "In progress",
		domain.SavedItemArchived:   "Archived",
		domain.SavedItemCompleted:  "Completed",
	}[state]
	data := laterData{
		Channel: channel, CSRFToken: auth.CSRFToken(sessionCookie.Value), State: state, StateTitle: title,
		InProgressCurrent: state == domain.SavedItemInProgress,
		ArchivedCurrent:   state == domain.SavedItemArchived,
		CompletedCurrent:  state == domain.SavedItemCompleted,
		Items:             make([]laterItemView, 0, len(page.Items)),
		RemindersOnly:     r.URL.Query().Get("filter") == "reminders",
		ChannelReminders:  r.URL.Query().Get("filter") == "channel-reminders",
	}
	if data.ChannelReminders {
		data.RemindersOnly = true
	}
	switch r.URL.Query().Get("changed") {
	case "saved":
		data.Notice = "Message saved for later."
	case "removed":
		data.Notice = "Message removed from Later."
	case "state":
		data.Notice = "Saved item moved."
	case "reminder":
		data.Notice = "Reminder saved."
	case "reminder-completed":
		data.Notice = "Reminder completed."
	case "reminder-deleted":
		data.Notice = "Reminder deleted."
	}
	reminderTarget := domain.LaterReminderPersonal
	if data.ChannelReminders {
		reminderTarget = domain.LaterReminderChannel
	}
	reminderPage, reminderErr := h.Messages.LaterReminders(r.Context(), principal.WorkspaceID, principal.UserID, reminderTarget, domain.PageRequest{Limit: scheduledWindow})
	if reminderErr != nil {
		h.writeStoreError(w, reminderErr, "Reminders are temporarily unavailable.")
		return
	}
	for _, reminder := range reminderPage.Items {
		completed := !reminder.CompletedAt.IsZero()
		if !data.ChannelReminders && (state == domain.SavedItemArchived || (state == domain.SavedItemCompleted) != completed) {
			continue
		}
		view := laterReminderView{
			ID: string(reminder.ID), Text: reminder.Text,
			MachineTime: reminder.DueAt.UTC().Format(time.RFC3339), DisplayTime: formatTime(reminder.DueAt),
			Recurrence: string(reminder.Recurrence), Delivered: !reminder.LastDeliveredAt.IsZero(), Completed: completed,
			Failed: !reminder.FailedAt.IsZero(), FailureCode: reminder.FailureCode,
			UpdateURL:   reminderActionURL("/app/reminders/update", reminder.ID, state, channel),
			CompleteURL: reminderActionURL("/app/reminders/complete", reminder.ID, state, channel),
			DeleteURL:   reminderActionURL("/app/reminders/delete", reminder.ID, state, channel),
			CanEdit:     reminder.Target == domain.LaterReminderPersonal && !completed,
			CanComplete: reminder.Target == domain.LaterReminderPersonal && !completed, TimeZone: reminder.TimeZone,
		}
		if reminder.Target == domain.LaterReminderChannel {
			view.SourceLabel = "Channel reminder"
			if conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, reminder.Channel); conversationErr == nil {
				view.SourceLabel = "#" + conversationName(conversation)
				view.SourceURL = "/app?channel=" + url.QueryEscape(string(reminder.Channel))
			}
		}
		if location, locationErr := time.LoadLocation(reminder.TimeZone); locationErr == nil {
			localDue := reminder.DueAt.In(location)
			view.DateValue = localDue.Format("2006-01-02")
			view.TimeValue = localDue.Format("15:04")
		}
		if reminder.SourceTimestamp != "" {
			sourceTime, parseErr := domain.ParseMessageTimestamp(reminder.SourceTimestamp)
			if parseErr == nil {
				boundary := domain.Message{ID: reminder.SourceMessageID, CreatedAt: sourceTime.Add(time.Nanosecond)}
				before := ""
				if cursor, cursorErr := domain.NewMessageCursor(boundary); cursorErr == nil {
					before = string(cursor)
				}
				view.SourceURL = appURL(string(reminder.SourceConversation), "", before, messageAnchor(reminder.SourceMessageID), "")
				view.SourceLabel = "View source message"
			}
		}
		data.Reminders = append(data.Reminders, view)
	}
	for _, item := range page.Items {
		view := laterItemView{
			ID:              string(item.ID),
			SourceAvailable: item.SourceAvailable,
			CompleteURL:     laterActionURL("/app/later/state", item.ID, domain.SavedItemCompleted, state, channel),
			ArchiveURL:      laterActionURL("/app/later/state", item.ID, domain.SavedItemArchived, state, channel),
			RestoreURL:      laterActionURL("/app/later/state", item.ID, domain.SavedItemInProgress, state, channel),
			RemoveURL:       laterActionURL("/app/later/remove", item.ID, "", state, channel),
		}
		if item.SourceAvailable {
			view.Text = item.Message.Text
			if strings.TrimSpace(view.Text) == "" {
				view.Text = "File or rich message"
			}
			view.MachineTime = item.Message.CreatedAt.UTC().Format(time.RFC3339Nano)
			view.DisplayTime = formatTime(item.Message.CreatedAt)
			boundary := item.Message
			boundary.CreatedAt = boundary.CreatedAt.Add(time.Nanosecond)
			before := ""
			if cursor, cursorErr := domain.NewMessageCursor(boundary); cursorErr == nil {
				before = string(cursor)
			}
			view.SourceURL = appURL(string(item.Conversation), string(item.Message.ThreadTimestamp), before, messageAnchor(item.Message.ID), "")
			view.AuthorName = "Unknown member"
			if author, authorErr := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Message.AuthorID); authorErr == nil {
				view.AuthorName = displayName(author)
			}
			view.ChannelName = "Conversation"
			if conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Conversation); conversationErr == nil {
				view.ChannelName = conversationName(conversation)
				view.ChannelPrefix = "#"
				if conversation.IsDirectOrGroup() {
					view.ChannelPrefix = ""
					if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
						view.ChannelName = participants
					}
				}
			}
		}
		data.Items = append(data.Items, view)
	}
	if page.HasMore && page.NextCursor != "" {
		query := url.Values{"channel": {channel}, "state": {string(state)}, "cursor": {string(page.NextCursor)}}
		data.MoreURL = "/app/later?" + query.Encode()
	}
	h.writeHTML(w, laterTemplate, data, http.StatusOK, "Later rendering unavailable")
}

func (h Handler) scheduledMessages(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	query.Set("tab", "scheduled")
	r.URL.RawQuery = query.Encode()
	h.draftsAndSent(w, r)
}

func (h Handler) draftsAndSent(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if !principal.HasScope(auth.ScopeChatWrite) {
		h.writeAuthError(w, r, auth.ErrMissingScope)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	if tab == "" {
		tab = "drafts"
	}
	if tab != "drafts" && tab != "scheduled" && tab != "sent" {
		h.writePageError(w, http.StatusBadRequest, "That Drafts & sent view is not valid", "Open Drafts & sent from the workspace and choose a tab.")
		return
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	data := draftsAndSentData{Channel: channel, CSRFToken: auth.CSRFToken(sessionCookie.Value), ActiveTab: tab}
	if r.URL.Query().Get("scheduled") == "1" {
		data.Notice = "Message scheduled."
	}
	if r.URL.Query().Get("cancelled") == "1" {
		data.Notice = "Scheduled message cancelled."
	}
	if r.URL.Query().Get("updated") == "1" {
		data.Notice = "Scheduled message updated."
	}
	if r.URL.Query().Get("sent") == "1" {
		data.Notice = "Scheduled message sent."
	}
	if r.URL.Query().Get("draft_deleted") == "1" {
		data.Notice = "Draft deleted."
	}
	if r.URL.Query().Get("draft_cleanup") == "failed" {
		data.Notice = "Message scheduled. Its old draft could not be cleared; delete that draft before scheduling it again."
	}
	conversationView := func(id domain.ConversationID, thread domain.MessageTimestamp) (string, string, string) {
		name, prefix, conversationURL := "Conversation unavailable", "", ""
		conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, id)
		if conversationErr == nil {
			name = conversationName(conversation)
			prefix = "#"
			if conversation.IsDirectOrGroup() {
				prefix = ""
				if participants := h.participantNames(r.Context(), principal, conversation.ID); participants != "" {
					name = participants
				}
			}
			conversationURL = appURL(string(id), string(thread), "", "", "")
		}
		return name, prefix, conversationURL
	}
	var next domain.Cursor
	var hasMore bool
	switch tab {
	case "drafts":
		page, pageErr := h.Messages.Drafts(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor, Descending: true})
		if pageErr != nil {
			err = pageErr
			break
		}
		data.Drafts = make([]draftView, 0, len(page.Items))
		for _, item := range page.Items {
			name, prefix, openURL := conversationView(item.ConversationID, item.ThreadTimestamp)
			deleteQuery := url.Values{"channel": {string(item.ConversationID)}, "thread_ts": {string(item.ThreadTimestamp)}, "return_channel": {channel}}
			data.Drafts = append(data.Drafts, draftView{
				Text: item.Text, MachineTime: item.UpdatedAt.UTC().Format(time.RFC3339Nano), DisplayTime: formatTime(item.UpdatedAt),
				ChannelName: name, ChannelPrefix: prefix, OpenURL: openURL,
				DeleteURL: "/app/draft/delete?" + deleteQuery.Encode(), AttachmentCount: len(item.Attachments),
			})
		}
		next, hasMore = page.NextCursor, page.HasMore
	case "scheduled":
		page, pageErr := h.Messages.ScheduledMessageHistory(r.Context(), principal.WorkspaceID, principal.UserID, false, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor, Descending: true})
		if pageErr != nil {
			err = pageErr
			break
		}
		data.Scheduled = make([]scheduledMessageView, 0, len(page.Items))
		for _, item := range page.Items {
			name, prefix, conversationURL := conversationView(item.Channel, item.ThreadTimestamp)
			displayText := item.Text
			if strings.TrimSpace(displayText) == "" && len(item.FileAttachments) != 0 {
				displayText = "File attachment"
			} else if strings.TrimSpace(displayText) == "" {
				displayText = "Rich message"
			}
			actionQuery := url.Values{
				"channel": {string(item.Channel)}, "id": {string(item.ID)}, "return_channel": {channel},
			}
			status, failure := "Scheduled", ""
			if !item.FailedAt.IsZero() {
				status = "Failed"
				failure = scheduledFailureMessage(item.FailureCode)
			}
			data.Scheduled = append(data.Scheduled, scheduledMessageView{
				ID: string(item.ID), Text: item.Text, DisplayText: displayText, MachineTime: item.PostAt.UTC().Format(time.RFC3339Nano),
				DisplayTime: formatTime(item.PostAt), ChannelName: name, ChannelPrefix: prefix,
				ConversationURL: conversationURL, Status: status, Failure: failure,
				AttachmentCount: len(item.FileAttachments),
				CancelURL:       "/app/message/schedule/cancel?" + actionQuery.Encode(),
				UpdateURL:       "/app/message/schedule/update?" + actionQuery.Encode(),
				SendNowURL:      "/app/message/schedule/send-now?" + actionQuery.Encode(),
			})
		}
		next, hasMore = page.NextCursor, page.HasMore
	case "sent":
		page, pageErr := h.Messages.SentMessages(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor, Descending: true})
		if pageErr != nil {
			err = pageErr
			break
		}
		data.Sent = make([]sentMessageView, 0, len(page.Messages))
		for _, item := range page.Messages {
			name, prefix, openURL := conversationView(item.Conversation, item.ThreadTimestamp)
			if openURL != "" {
				openURL = appURL(string(item.Conversation), string(item.ThreadTimestamp), "", messageAnchor(item.ID), "")
			}
			data.Sent = append(data.Sent, sentMessageView{
				Text: item.Text, MachineTime: item.CreatedAt.UTC().Format(time.RFC3339Nano), DisplayTime: formatTime(item.CreatedAt),
				ChannelName: name, ChannelPrefix: prefix, OpenURL: openURL,
			})
		}
		next, hasMore = page.NextCursor, page.HasMore
	}
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) || errors.Is(err, store.ErrInvalidArgument) {
			h.writePageError(w, http.StatusBadRequest, "That Drafts & sent link is not valid", "Open Drafts & sent from the workspace and try again.")
			return
		}
		h.writeStoreError(w, err, "Drafts & sent is temporarily unavailable.")
		return
	}
	if hasMore && next != "" {
		query := url.Values{"channel": {channel}, "tab": {tab}, "cursor": {string(next)}}
		data.MoreURL = "/app/drafts?" + query.Encode()
		data.MoreLabel = map[string]string{
			"drafts":    "Show more drafts",
			"scheduled": "Show more scheduled messages",
			"sent":      "Show more sent messages",
		}[tab]
	}
	h.writeHTML(w, draftsAndSentTemplate, data, http.StatusOK, "Drafts & sent rendering unavailable")
}

func scheduledFailureMessage(code string) string {
	switch code {
	case "not_in_channel":
		return "You are no longer a member of this conversation."
	case "is_archived":
		return "This conversation is archived."
	case "invalid_thread_ts":
		return "The thread is no longer available."
	case "channel_not_found":
		return "The conversation is no longer available."
	case "invalid_arguments":
		return "The message content is no longer valid."
	default:
		return "Slack could not deliver this message."
	}
}

func (h Handler) activity(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	// Preferences carry the member's saved views, so they are read before the
	// query: a saved view resolves to the same Kinds a single filter tab does.
	preferences, err := h.Messages.ActivityPreferences(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your Activity layout is temporarily unavailable.")
		return
	}
	kindValue := strings.TrimSpace(r.URL.Query().Get("kind"))
	viewValue := strings.TrimSpace(r.URL.Query().Get("view"))
	kindByValue := map[string]domain.ActivityKind{
		"dm": domain.ActivityDM, "mention": domain.ActivityMention, "thread": domain.ActivityThread,
		"channel": domain.ActivityChannel, "reaction": domain.ActivityReaction, "invitation": domain.ActivityInvitation,
		"app": domain.ActivityApp, "reminder": domain.ActivityReminder,
	}
	var kinds []domain.ActivityKind
	switch {
	case viewValue != "":
		// A saved view names its own kinds; it and a single filter do not combine.
		found := false
		for _, view := range preferences.SavedViews {
			if string(view.ID) == viewValue {
				kinds = view.Kinds
				found = true
				break
			}
		}
		if !found {
			h.writePageError(w, http.StatusBadRequest, "That saved view is not available", "Open Activity again and choose one of your views.")
			return
		}
		kindValue = ""
	case kindValue != "":
		kind, ok := kindByValue[kindValue]
		if !ok {
			h.writePageError(w, http.StatusBadRequest, "That Activity filter is not valid", "Open Activity again and choose one of the available filters.")
			return
		}
		kinds = []domain.ActivityKind{kind}
	}
	unreadOnly := r.URL.Query().Get("unread") == "1"
	clearedOnly := r.URL.Query().Get("cleared") == "1"
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.Activity(r.Context(), principal.WorkspaceID, principal.UserID, domain.ActivityQuery{
		Kinds: kinds, UnreadOnly: unreadOnly, ClearedOnly: clearedOnly,
		Page: domain.PageRequest{Limit: 50, Cursor: cursor},
	})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			h.writePageError(w, http.StatusBadRequest, "That Activity link is no longer valid", "Open Activity again to see the latest items.")
			return
		}
		h.writeStoreError(w, err, "Activity is temporarily unavailable.")
		return
	}
	data := activityData{
		Channel: channel, Kind: kindValue, View: viewValue, UnreadOnly: unreadOnly, ClearedOnly: clearedOnly,
		Layout: string(preferences.Layout), CreateURL: "/app/activity/views?channel=" + url.QueryEscape(channel),
		DeleteURL: "/app/activity/views/delete?channel=" + url.QueryEscape(channel),
	}
	if sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil && strings.TrimSpace(sessionCookie.Value) != "" {
		data.CSRFToken = auth.CSRFToken(sessionCookie.Value)
	}
	names := h.newUserNames(r.Context(), principal)
	for _, item := range page.Items {
		view := activityItemView{
			ID: string(item.ID), KindLabel: activityKindLabel(item), ActorName: names.name(item.ActorID),
			MachineTime: item.OccurredAt.UTC().Format(time.RFC3339Nano), DisplayTime: formatTime(item.OccurredAt),
			Unread: item.ReadAt.IsZero(), Cleared: !item.ClearedAt.IsZero(),
		}
		if item.ReminderID != "" {
			view.ActorName = "Reminder"
			if item.Reminder.ID != "" {
				view.Text = template.HTML(template.HTMLEscapeString(item.Reminder.Text))
				view.SourceURL = "/app/later?channel=" + url.QueryEscape(channel) + "&state=completed"
			}
		}
		// A reminders.add reminder carries its own text and no source message,
		// so the row says what the member asked to be reminded of and links
		// nowhere rather than to a message that does not exist.
		if item.AppReminderID != "" {
			view.ActorName = "Reminder"
			if item.AppReminder.ID != "" {
				view.Text = template.HTML(template.HTMLEscapeString(item.AppReminder.Text))
			}
		}
		if item.Message.ID != "" {
			messageViews := h.newResultViews(r.Context(), principal, []domain.Message{item.Message}, names)
			if len(messageViews) == 1 {
				message := messageViews[0]
				if item.ReminderID == "" {
					view.Text = message.DisplayText
				}
				view.SourceURL = message.Permalink
				replyThread := string(item.Message.ThreadTimestamp)
				if replyThread == "" {
					replyThread = string(domain.NewMessageTimestamp(item.Message.CreatedAt))
				}
				view.ReplyURL = appURL(string(item.Message.Conversation), replyThread, "", "", "")
				view.ChannelName = message.ChannelPrefix + message.ChannelName
				if principal.HasScope(auth.ScopeReactionsWrite) {
					view.ReactionURL = mutationURL(
						"/app/reaction",
						string(item.Message.Conversation),
						string(domain.NewMessageTimestamp(item.Message.CreatedAt)),
						string(item.Message.ThreadTimestamp),
						"",
					)
				}
				if item.ReminderID == "" {
					view.ActorName = message.AuthorName
				}
			}
		}
		if item.SharedInviteID != "" && item.SourceAvailable {
			// The two kinds of channel news read differently on purpose: being
			// added to a channel and having your request to invite an
			// organization decided are not the same thing to be told.
			name := "a channel"
			if conversation, convErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Conversation); convErr == nil {
				name = conversationName(conversation)
				if !strings.HasPrefix(name, "#") {
					name = "#" + name
				}
				view.SourceURL = appURL(string(conversation.ID), "", "", "", "")
				view.ChannelName = name
			}
			decided := string(item.SharedInviteStatus)
			if decided == "" {
				decided = "decided"
			}
			view.Text = template.HTML("Your Slack Connect invitation for " + template.HTMLEscapeString(name) + " was " + template.HTMLEscapeString(decided) + ".")
		}
		if item.ListItemID != "" && item.SourceAvailable {
			name := strings.TrimSpace(item.ListName)
			if name == "" {
				name = "a list"
			}
			text := "Assigned you an item in " + name + "."
			if !item.ListItem.DueAt.IsZero() {
				text = "Assigned you an item in " + name + ", due " + formatTime(item.ListItem.DueAt) + "."
			}
			if item.ListItem.Overdue(time.Now()) {
				text += " It is overdue."
			}
			view.Text = template.HTML(template.HTMLEscapeString(text))
			view.SourceURL = "/app/lists/" + url.PathEscape(string(item.ListID))
			view.ChannelName = name
		}
		if item.CanvasID != "" && item.SourceAvailable {
			// A canvas share is an invitation from a different object, so it
			// shares the kind and differs only in what it names and where it
			// goes. The title comes from the read rather than a second lookup,
			// because the store already resolved it under the access rule that
			// decided whether this row is reachable at all.
			title := strings.TrimSpace(item.CanvasTitle)
			if title == "" {
				title = "a canvas"
			}
			view.Text = template.HTML("Shared " + template.HTMLEscapeString(title) + " with you.")
			view.SourceURL = "/app/canvases/" + url.PathEscape(string(item.CanvasID))
			view.ChannelName = title
		}
		if item.CanvasID == "" && item.ListItemID == "" && item.SharedInviteID == "" && slices.Contains(item.Kinds, domain.ActivityInvitation) && item.SourceAvailable {
			conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, item.Conversation)
			if conversationErr == nil {
				name := conversationName(conversation)
				if !strings.HasPrefix(name, "#") {
					name = "#" + name
				}
				view.Text = template.HTML("Added you to " + template.HTMLEscapeString(name) + ".")
				view.SourceURL = appURL(string(conversation.ID), "", "", "", "")
				view.ChannelName = name
			} else if !errors.Is(conversationErr, store.ErrNotFound) {
				h.writeStoreError(w, conversationErr, "Activity is temporarily unavailable.")
				return
			}
		}
		if !item.SourceAvailable || view.Text == "" {
			view.Text = "This activity’s source is no longer available."
			view.Unavailable = true
			view.SourceURL = ""
		}
		data.Items = append(data.Items, view)
	}
	filterDefinitions := []struct{ value, label string }{
		{"", "All"}, {"dm", "DMs"}, {"mention", "Mentions"}, {"thread", "Threads"},
		{"channel", "Channels"}, {"reaction", "Reactions"}, {"invitation", "Invitations"}, {"app", "Apps"}, {"reminder", "Reminders"},
	}
	for _, filter := range filterDefinitions {
		data.Filters = append(data.Filters, activityFilterView{
			Label: filter.label, Current: viewValue == "" && kindValue == filter.value,
			URL: activityPageURL(channel, filter.value, "", unreadOnly, clearedOnly, ""),
		})
		if filter.value != "" {
			data.KindOptions = append(data.KindOptions, activityKindOption{Value: filter.value, Label: filter.label})
		}
	}
	for _, view := range preferences.SavedViews {
		data.SavedViews = append(data.SavedViews, activitySavedViewView{
			ID: string(view.ID), Name: view.Name, Current: viewValue == string(view.ID),
			URL: activityPageURL(channel, "", string(view.ID), false, false, ""),
		})
	}
	data.UnreadURL = activityPageURL(channel, kindValue, viewValue, !unreadOnly, clearedOnly, "")
	data.ClearedURL = activityPageURL(channel, kindValue, viewValue, false, true, "")
	data.ActiveURL = activityPageURL(channel, kindValue, viewValue, false, false, "")
	if page.HasMore && page.NextCursor != "" {
		data.MoreURL = activityPageURL(channel, kindValue, viewValue, unreadOnly, clearedOnly, page.NextCursor)
	}
	h.writeHTML(w, activityTemplate, data, http.StatusOK, "activity rendering unavailable")
}

func activityKindLabel(item domain.ActivityItem) string {
	labels := make([]string, 0, len(item.Kinds))
	for _, kind := range item.Kinds {
		label := string(kind)
		switch kind {
		case domain.ActivityDM:
			label = "DM"
		case domain.ActivityMention:
			label = "Mention"
		case domain.ActivityThread:
			label = "Thread"
		case domain.ActivityChannel:
			label = "Channel"
		case domain.ActivityKeyword:
			label = "Keyword"
		case domain.ActivityReaction:
			label = "Reaction"
			if item.ReactionName != "" {
				label += " :" + item.ReactionName + ":"
			}
		case domain.ActivityApp:
			label = "App"
		case domain.ActivityReminder:
			label = "Reminder"
		case domain.ActivityInvitation:
			label = "Invitation"
		}
		labels = append(labels, label)
	}
	return strings.Join(labels, " · ")
}

func activityPageURL(channel, kind, view string, unread, cleared bool, cursor domain.Cursor) string {
	values := url.Values{"channel": {channel}}
	if kind != "" {
		values.Set("kind", kind)
	}
	if view != "" {
		values.Set("view", view)
	}
	if unread {
		values.Set("unread", "1")
	}
	if cleared {
		values.Set("cleared", "1")
	}
	if cursor != "" {
		values.Set("cursor", string(cursor))
	}
	return "/app/activity?" + values.Encode()
}

func (h Handler) mutateActivity(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	if err := r.ParseForm(); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That Activity action could not be read", "Reload Activity and try again.")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	mutationValue := strings.TrimSpace(r.URL.Query().Get("mutation"))
	if mutationValue == "" {
		mutationValue = strings.TrimSpace(r.FormValue("mutation"))
	}
	mutation := domain.ActivityMutation(mutationValue)
	if !mutation.Valid() {
		h.writeMutationError(w, r, http.StatusBadRequest, "That Activity action is not valid", "Choose Mark read, Mark unread, Clear, or Restore and try again.")
		return
	}
	idValues := r.Form["activity_id"]
	if single := strings.TrimSpace(r.FormValue("single_id")); single != "" {
		idValues = []string{single}
	}
	ids := make([]domain.ActivityID, 0, len(idValues))
	seen := make(map[domain.ActivityID]struct{}, len(idValues))
	for _, value := range idValues {
		id := domain.ActivityID(strings.TrimSpace(value))
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		h.writeMutationError(w, r, http.StatusBadRequest, "Select at least one Activity item", "No Activity was changed.")
		return
	}
	if err := h.Messages.MutateActivity(r.Context(), principal.WorkspaceID, principal.UserID, ids, mutation); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.writeMutationError(w, r, http.StatusNotFound, "That Activity item is no longer available", "Reload Activity to see the latest items.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Activity is temporarily unavailable", "No Activity item was changed.")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	h.redirectMutation(w, r, activityPageURL(
		channel, strings.TrimSpace(r.FormValue("kind")), strings.TrimSpace(r.FormValue("view")),
		r.FormValue("unread") == "1", r.FormValue("cleared") == "1", "",
	))
}

func (h Handler) setActivityPreferences(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Activity and choose a layout again.")
	if !ok {
		return
	}
	layout := domain.ActivityLayout(strings.TrimSpace(fields["layout"]))
	if !layout.Valid() {
		h.writeMutationError(w, r, http.StatusBadRequest, "That Activity layout is not valid", "Choose Detailed or Dense.")
		return
	}
	if _, err := h.Messages.SetActivityPreferences(r.Context(), principal.WorkspaceID, principal.UserID, layout); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Your Activity layout could not be saved", "Reload Activity and try again.")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	h.redirectMutation(w, r, activityPageURL(
		channel, strings.TrimSpace(fields["kind"]), strings.TrimSpace(fields["view"]),
		fields["unread"] == "1", fields["cleared"] == "1", "",
	))
}

// createActivitySavedView records a member's named Activity filter from the
// create form's name and checked kinds, then returns to the new view.
func (h Handler) createActivitySavedView(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, kindValues, err := decodeFormValues(w, r, "kind")
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That form could not be read", "Reload Activity and try again.")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	kinds := make([]domain.ActivityKind, 0, len(kindValues))
	for _, value := range kindValues {
		kinds = append(kinds, domain.ActivityKind(strings.TrimSpace(value)))
	}
	view, err := h.Messages.CreateActivitySavedView(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], kinds)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That view was not saved", "Give the view a name and choose at least one kind, and check you have room for another.")
		return
	}
	h.redirectMutation(w, r, activityPageURL(channel, "", string(view.ID), false, false, ""))
}

// deleteActivitySavedView removes a member's saved view and returns to All.
func (h Handler) deleteActivitySavedView(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Activity and try again.")
	if !ok {
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	if err := h.Messages.DeleteActivitySavedView(r.Context(), principal.WorkspaceID, principal.UserID, domain.ActivitySavedViewID(strings.TrimSpace(fields["view_id"]))); err != nil {
		h.writeMutationError(w, r, http.StatusNotFound, "That view was not removed", "It is no longer there, or it is not yours to remove.")
		return
	}
	h.redirectMutation(w, r, activityPageURL(channel, "", "", false, false, ""))
}

func (h Handler) acknowledgeActivityReminders(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The reminder read marker could not be read. Reload Activity and try again."); !ok {
		return
	}
	if err := h.Messages.AcknowledgeLaterReminders(r.Context(), principal.WorkspaceID, principal.UserID); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Reminder badges are temporarily unavailable", "The reminder read marker could not be saved. No reminder was changed.")
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	h.redirectMutation(w, r, "/app/activity?channel="+url.QueryEscape(channel))
}

func (h Handler) notifications(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	preferences, err := h.Messages.WorkspaceNotificationPreferences(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your notification preferences are temporarily unavailable.")
		return
	}
	dnd, err := h.Messages.DoNotDisturbInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your notification pause is temporarily unavailable.")
		return
	}
	data := notificationsData{
		Channel: channel, CSRFToken: auth.CSRFToken(sessionCookie.Value),
		Level: string(preferences.Level), Keywords: strings.Join(preferences.Keywords, ", "),
		ActivityChannels: preferences.ActivityChannels, ActivityReminders: preferences.ActivityReminders,
		BrowserNotifications: preferences.BrowserNotifications,
		// The server knows only its own half. The script replaces this line
		// with what the browser actually reports, which is the only way to
		// tell "you have not turned it on" from "your browser refused".
		BrowserNotificationState: "Your browser also has to allow notifications. This page will say which of the two is missing.",
		Snoozed:                  dnd.SnoozeUntil.After(time.Now()),
		SnoozeUntil:              dnd.SnoozeUntil.UTC().Format(time.RFC3339),
		ScheduleEnabled:          preferences.Schedule.Enabled,
		ScheduleStart:            minutesAsClock(preferences.Schedule.StartMinute),
		ScheduleEnd:              minutesAsClock(preferences.Schedule.EndMinute),
		ScheduleZone:             preferences.Schedule.TimeZone,
		ScheduleDays:             scheduleDayViews(preferences.Schedule.Days),
		// Saying only that a schedule exists would leave a member wondering why
		// nothing arrives at four in the afternoon. The page says which side of
		// the window it is on, now, in their own zone.
		ScheduleSuppressing: preferences.Schedule.Enabled && !preferences.Schedule.AllowsAt(time.Now()),
	}
	switch r.URL.Query().Get("status") {
	case "saved":
		data.Notice = "Notification preferences saved."
	case "paused":
		data.Notice = "Notifications paused. Messages and Activity remain available."
	case "resumed":
		data.Notice = "Notifications resumed."
	}

	var cursor domain.Cursor
	unreadableExceptions := 0
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		page, listErr := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
			Limit: 100, Cursor: cursor, MemberUserID: principal.UserID,
		})
		if listErr != nil {
			h.writeStoreError(w, listErr, "Conversation notification exceptions are temporarily unavailable.")
			return
		}
		for _, conversation := range page.Conversations {
			// DMs always belong in Activity. The channel override controls are
			// intentionally limited to channels until banner delivery itself is
			// represented by this client.
			if conversation.IsDirectOrGroup() {
				continue
			}
			override, preferenceErr := h.Messages.ConversationNotificationPreferences(
				r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID,
			)
			if preferenceErr != nil {
				// One conversation whose override cannot be read must not take
				// down the whole page. The workspace defaults, the pause
				// control and every other exception have nothing to do with
				// it, and refusing all of them leaves a person unable to
				// change the settings that do work. The page says an exception
				// is missing rather than pretending the list is complete.
				unreadableExceptions++
				continue
			}
			if override.Level == domain.NotificationInherit && !override.FollowEveryThread {
				continue
			}
			data.Exceptions = append(data.Exceptions, notificationExceptionView{
				ID: string(conversation.ID), Name: conversationName(conversation), Prefix: "#",
				Level: string(override.Level), FollowEveryThread: override.FollowEveryThread,
				URL: conversationDetailsURL(conversation.ID) + "#conversation-notifications",
			})
		}
		if !page.HasMore || page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pageNumber == 9 {
			data.Notice = strings.TrimSpace(data.Notice + " Only the first 1,000 conversation exceptions are shown.")
		}
	}
	if unreadableExceptions > 0 {
		data.Notice = strings.TrimSpace(data.Notice + " " + strconv.Itoa(unreadableExceptions) + " conversation exception(s) could not be read and are not listed.")
	}
	sort.Slice(data.Exceptions, func(left, right int) bool {
		return strings.ToLower(data.Exceptions[left].Name) < strings.ToLower(data.Exceptions[right].Name)
	})
	h.writeHTML(w, notificationsTemplate, data, http.StatusOK, "notification preferences rendering unavailable")
}

func splitNotificationKeywords(value string) []string {
	parts := strings.Split(value, ",")
	keywords := make([]string, 0, len(parts))
	for _, part := range parts {
		if keyword := strings.TrimSpace(part); keyword != "" {
			keywords = append(keywords, keyword)
		}
	}
	return keywords
}

func (h Handler) setWorkspaceNotifications(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Notifications and try again.")
	if !ok {
		return
	}
	if _, err := h.Messages.SetWorkspaceNotificationPreferences(
		r.Context(), principal.WorkspaceID, principal.UserID,
		domain.NotificationLevel(strings.TrimSpace(fields["level"])),
		splitNotificationKeywords(fields["keywords"]),
		fields["activity_channels"] == "true", fields["activity_reminders"] == "true",
		fields["browser_notifications"] == "true",
	); err != nil {
		if errors.Is(err, store.ErrInvalidArgument) {
			h.writeMutationError(w, r, http.StatusBadRequest, "Those notification preferences are not valid", "Choose a trigger and use no more than 50 keywords of 100 characters each.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "Notification preferences were not saved", "The workspace store is temporarily unavailable.")
		return
	}
	h.redirectMutation(w, r, "/app/notifications?channel="+url.QueryEscape(string(h.requestChannel(r)))+"&status=saved")
}

// setNotificationSchedule writes the window a member allows notifications in.
// The browser supplies its own zone, as it already does for a scheduled message
// and a reminder: a schedule is a statement about the member's day, and the
// server's clock is not their day.
// setNotificationVIP toggles a person on the viewing member's VIP list from the
// directory. Marking yourself or someone who is not here is refused; the page
// returns to the directory either way.
func (h Handler) setNotificationVIP(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the directory and try again.")
	if !ok {
		return
	}
	target := domain.UserID(strings.TrimSpace(fields["target"]))
	if err := h.Messages.SetNotificationVIP(r.Context(), principal.WorkspaceID, principal.UserID, target, fields["add"] == "true"); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That VIP change was not saved", "Choose another member of this workspace and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/members")
}

func (h Handler) setNotificationSchedule(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	// day repeats: the form offers one checkbox per weekday, and choosing
	// several is the ordinary case. decodeMutation's single-occurrence rule
	// would reject the whole request, which is how the first version of this
	// handler silently refused every schedule covering more than one day.
	fields, dayValues, err := decodeFormValues(w, r, "day")
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That form could not be read", "Reload Notifications and try again.")
		return
	}
	if !h.requireCSRF(w, r) {
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	schedule := domain.NotificationSchedule{Enabled: fields["enabled"] == "true", TimeZone: strings.TrimSpace(fields["timezone"])}
	if schedule.Enabled {
		start, startOK := clockAsMinutes(fields["start"])
		end, endOK := clockAsMinutes(fields["end"])
		if !startOK || !endOK {
			h.writeMutationError(w, r, http.StatusBadRequest, "The schedule was not saved", "Enter a start and end time.")
			return
		}
		schedule.StartMinute, schedule.EndMinute = start, end
		for _, raw := range dayValues {
			number, convErr := strconv.Atoi(strings.TrimSpace(raw))
			if convErr != nil {
				h.writeMutationError(w, r, http.StatusBadRequest, "The schedule was not saved", "Choose the days the schedule applies on.")
				return
			}
			schedule.Days = append(schedule.Days, time.Weekday(number))
		}
	}
	if _, err := h.Messages.SetNotificationSchedule(r.Context(), principal.WorkspaceID, principal.UserID, schedule); err != nil {
		// A schedule that allows nothing, or a window of zero length, is a
		// refusal rather than a silent save: the member would otherwise stop
		// hearing anything and have no way to see why.
		h.writeMutationError(w, r, http.StatusBadRequest, "The schedule was not saved", "Choose at least one day and a start time different from the end time.")
		return
	}
	h.redirectMutation(w, r, "/app/notifications?channel="+url.QueryEscape(channel)+"&status=saved")
}

func (h Handler) setNotificationSnooze(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Notifications and try again.")
	if !ok {
		return
	}
	status := "paused"
	switch strings.TrimSpace(fields["action"]) {
	case "resume":
		_, err = h.Messages.EndSnooze(r.Context(), principal.WorkspaceID, principal.UserID)
		status = "resumed"
	case "pause":
		minutesValue := strings.TrimSpace(fields["custom_minutes"])
		if minutesValue == "" {
			minutesValue = strings.TrimSpace(fields["minutes"])
		}
		var minutes int64
		minutes, err = strconv.ParseInt(minutesValue, 10, 64)
		if err == nil {
			_, err = h.Messages.SetSnooze(r.Context(), principal.WorkspaceID, principal.UserID, minutes)
		}
	default:
		h.writeMutationError(w, r, http.StatusBadRequest, "That notification pause is not valid", "Choose a pause duration or Resume notifications.")
		return
	}
	if err != nil {
		if errors.Is(err, service.ErrInvalidSnooze) || errors.Is(err, strconv.ErrSyntax) {
			h.writeMutationError(w, r, http.StatusBadRequest, "That pause duration is not valid", "Choose between 1 minute and 24 hours.")
			return
		}
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "The notification pause was not changed", "The workspace store is temporarily unavailable.")
		return
	}
	h.redirectMutation(w, r, "/app/notifications?channel="+url.QueryEscape(string(h.requestChannel(r)))+"&status="+status)
}

func (h Handler) hasUnacknowledgedReminder(ctx context.Context, principal auth.Principal) (bool, error) {
	var cursor domain.Cursor
	for {
		page, err := h.Messages.LaterReminders(ctx, principal.WorkspaceID, principal.UserID, domain.LaterReminderPersonal, domain.PageRequest{Limit: scheduledWindow, Cursor: cursor})
		if err != nil {
			return false, err
		}
		for _, reminder := range page.Items {
			if reminder.LastDeliveredAt.After(reminder.AcknowledgedAt) {
				return true, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
			return false, nil
		}
		cursor = page.NextCursor
	}
}

func (h Handler) search(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	resultType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("type")))
	switch resultType {
	case "", "messages":
		resultType = "messages"
	case "files", "canvases", "lists", "people", "channels":
	default:
		resultType = "messages"
	}
	sortOrder, direction := domain.SearchSortScore, domain.SearchDirectionDescending
	switch r.URL.Query().Get("order") {
	case "newest":
		sortOrder = domain.SearchSortTimestamp
	case "oldest":
		sortOrder, direction = domain.SearchSortTimestamp, domain.SearchDirectionAscending
	}
	data := searchData{
		Query: query, Channel: channel, Type: resultType, Searched: query != "",
		Sort: string(sortOrder), Direction: string(direction),
		SelectedConversation: strings.TrimSpace(r.URL.Query().Get("in")),
		SelectedMember:       strings.TrimSpace(r.URL.Query().Get("from")),
		After:                strings.TrimSpace(r.URL.Query().Get("after")),
		Before:               strings.TrimSpace(r.URL.Query().Get("before")),
		Has:                  strings.TrimSpace(r.URL.Query().Get("has")),
		CurrentOnly:          r.URL.Query().Get("scope") == "channel",
	}
	if query == "" {
		recent, recentErr := h.Messages.RecentSearches(r.Context(), principal.WorkspaceID, principal.UserID, recentSearchWindow)
		if recentErr != nil {
			h.writeStoreError(w, recentErr, "Recent searches are temporarily unavailable.")
			return
		}
		data.Recent = searchHistoryViews(recent, channel)
		h.writeHTML(w, searchTemplate, data, http.StatusOK, "search rendering unavailable")
		return
	}
	data.MemberOptions, data.ConversationOptions, err = h.searchFilterOptions(r.Context(), principal)
	if err != nil {
		h.writeStoreError(w, err, "Search filters are temporarily unavailable.")
		return
	}
	for _, tab := range []struct{ value, label string }{{"messages", "Messages"}, {"files", "Files"}, {"canvases", "Canvases"}, {"lists", "Lists"}, {"people", "People"}, {"channels", "Channels"}} {
		values := cloneURLValues(r.URL.Query())
		values.Set("type", tab.value)
		values.Del("cursor")
		values.Del("page")
		data.Tabs = append(data.Tabs, searchTabView{Label: tab.label, URL: "/app/search?" + values.Encode(), Current: resultType == tab.value})
	}
	effectiveQuery := searchQueryWithFilters(query, data)
	// The tokens are the terms a result marks. An unterminated phrase is still a
	// bad query, and refusing it here keeps that answer the same on every tab.
	textTokens, tokenErr := domain.SearchQueryTokens(query)
	if tokenErr != nil {
		h.writeSearchError(w, data, service.ErrInvalidSearch)
		return
	}
	// A modifier is an instruction, not a word anybody is looking for: marking
	// "from:" inside a message body would emphasise text the member never
	// searched for.
	terms := searchableTerms(textTokens)
	switch resultType {
	case "messages":
		request := domain.MessageSearchRequest{
			Query: effectiveQuery, Sort: sortOrder, Direction: direction,
			Page: domain.PageRequest{Limit: searchWindow, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))},
		}
		if data.CurrentOnly {
			request.Conversation = domain.ConversationID(channel)
		}
		results, searchErr := h.Messages.SearchMessages(r.Context(), principal.WorkspaceID, principal.UserID, request)
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		data.Messages = h.newResultViews(r.Context(), principal, results.Messages, h.newUserNames(r.Context(), principal), terms...)
		data.ResultCount = results.Total
		if results.HasMore && results.NextCursor != "" {
			values := cloneURLValues(r.URL.Query())
			values.Set("cursor", string(results.NextCursor))
			data.MoreURL = "/app/search?" + values.Encode()
		}
	case "files":
		pageNumber := 1
		if raw := strings.TrimSpace(r.URL.Query().Get("page")); raw != "" {
			pageNumber, err = strconv.Atoi(raw)
		}
		if err != nil || pageNumber <= 0 || pageNumber > 100 {
			h.writeSearchError(w, data, domain.ErrInvalidCursor)
			return
		}
		request := domain.FileSearchRequest{
			Query: effectiveQuery, Sort: sortOrder, Direction: direction, Count: searchWindow, Page: pageNumber,
		}
		if data.CurrentOnly {
			request.Conversation = domain.ConversationID(channel)
		}
		results, searchErr := h.Messages.SearchFiles(r.Context(), principal.WorkspaceID, principal.UserID, request)
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		names := h.newUserNames(r.Context(), principal)
		for _, file := range results.Files {
			data.Files = append(data.Files, searchFileView{
				ID: string(file.ID), Name: markedText(file.Name, terms), Title: markedText(file.Title, terms), MIMEType: file.MIMEType,
				Size: formatFileSize(file.Size), Uploader: names.name(file.Uploader),
				DisplayTime: formatTime(file.CreatedAt), MachineTime: file.CreatedAt.UTC().Format(time.RFC3339Nano),
				DownloadURL: "/api/files/" + url.PathEscape(string(file.ID)),
			})
		}
		data.ResultCount = results.Total
		if results.HasMore {
			values := cloneURLValues(r.URL.Query())
			values.Set("page", strconv.Itoa(pageNumber+1))
			data.MoreURL = "/app/search?" + values.Encode()
		}
	case "people":
		page, searchErr := h.Messages.SearchPeople(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{
			Limit: searchWindow, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor"))),
		})
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		for _, member := range page.Users {
			if member.Deleted {
				continue
			}
			name := displayName(member)
			data.People = append(data.People, memberView{
				ID: string(member.ID), Name: name, RealName: member.RealName,
				MarkedName:    markedText(name, terms),
				AuthorInitial: initial(name), IsSelf: member.ID == principal.UserID,
			})
		}
		data.ResultCount = len(data.People)
		data.MoreURL = searchPageURL(r, page.HasMore, string(page.NextCursor))
	case "canvases":
		results, searchErr := h.Messages.SearchCanvases(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasSearchRequest{
			Query: effectiveQuery, Sort: sortOrder, Direction: direction,
			Page: domain.PageRequest{Limit: searchWindow, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))},
		})
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		names := h.newUserNames(r.Context(), principal)
		for _, canvas := range results.Canvases {
			data.Canvases = append(data.Canvases, searchCanvasView{
				ID: string(canvas.ID), Title: markedText(canvas.Title, terms),
				Snippet:     markedText(canvasSearchSnippet(canvas, terms), terms),
				Owner:       names.name(canvas.OwnerID),
				DisplayTime: canvas.UpdatedAt.Format("Jan 2, 15:04"),
				MachineTime: canvas.UpdatedAt.UTC().Format(time.RFC3339),
				URL:         "/app/canvases/" + url.PathEscape(string(canvas.ID)),
			})
		}
		data.ResultCount = len(data.Canvases)
		if results.HasMore && results.NextCursor != "" {
			values := cloneURLValues(r.URL.Query())
			values.Set("cursor", string(results.NextCursor))
			data.MoreURL = "/app/search?" + values.Encode()
		}
	case "lists":
		results, searchErr := h.Messages.SearchLists(r.Context(), principal.WorkspaceID, principal.UserID, domain.ListSearchRequest{
			Query: effectiveQuery, Sort: sortOrder, Direction: direction,
			Page: domain.PageRequest{Limit: searchWindow, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))},
		})
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		names := h.newUserNames(r.Context(), principal)
		for _, list := range results.Lists {
			data.Lists = append(data.Lists, searchListView{
				ID: string(list.ID), Title: markedText(list.Name, terms),
				Snippet:     markedText(listSearchSnippet(list, terms), terms),
				Owner:       names.name(list.OwnerID),
				DisplayTime: list.UpdatedAt.Format("Jan 2, 15:04"),
				MachineTime: list.UpdatedAt.UTC().Format(time.RFC3339),
				URL:         "/app/lists/" + url.PathEscape(string(list.ID)),
			})
		}
		data.ResultCount = len(data.Lists)
		if results.HasMore && results.NextCursor != "" {
			values := cloneURLValues(r.URL.Query())
			values.Set("cursor", string(results.NextCursor))
			data.MoreURL = "/app/search?" + values.Encode()
		}
	case "channels":
		page, searchErr := h.Messages.SearchChannels(r.Context(), principal.WorkspaceID, principal.UserID, query, domain.PageRequest{
			Limit: searchWindow, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor"))),
		})
		if searchErr != nil {
			h.writeSearchError(w, data, searchErr)
			return
		}
		for _, conversation := range page.Conversations {
			name := conversationName(conversation)
			data.Conversations = append(data.Conversations, conversationView{ID: string(conversation.ID), Name: name, MarkedName: markedText(name, terms)})
		}
		data.ResultCount = len(data.Conversations)
		data.MoreURL = searchPageURL(r, page.HasMore, string(page.NextCursor))
	}
	if err := h.Messages.RecordSearch(r.Context(), principal.WorkspaceID, principal.UserID, query); err != nil {
		data.Warning = "Search completed, but it could not be added to recent searches."
	}
	h.writeHTML(w, searchTemplate, data, http.StatusOK, "search rendering unavailable")
}

// canvasSearchSnippet is the first prose in the document, bounded. It is not a
// highlighted match: Slack marks the matched span and this does not, which is
// recorded as a deviation rather than faked with a substring search that would
// mark the wrong span whenever a term matched the title instead of the body.
// canvasSearchSnippet is the window of the document a result shows. It used to
// be the opening of the body, which is the wrong window when the term is on
// page four: the result would show prose that does not contain what was
// searched for and read as a mismatch. It now centres on the first match, and
// falls back to the opening when the match is in the title.
func canvasSearchSnippet(canvas domain.Canvas, terms []string) string {
	text := strings.TrimSpace(strings.TrimPrefix(domain.CanvasSearchText(canvas.Title, canvas.DocumentContent), canvas.Title))
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= canvasSnippetRunes {
		return text
	}
	start := 0
	if spans := matchedSpans(strings.ToLower(text), terms); len(spans) > 0 && len(strings.ToLower(text)) == len(text) {
		// Half the window before the match, so the hit sits in the middle
		// rather than against an edge where it reads as truncated.
		start = len([]rune(text[:spans[0].start])) - canvasSnippetRunes/2
	}
	if start < 0 {
		start = 0
	}
	if start+canvasSnippetRunes > len(runes) {
		start = len(runes) - canvasSnippetRunes
	}
	snippet := strings.TrimSpace(string(runes[start : start+canvasSnippetRunes]))
	if start > 0 {
		snippet = "…" + snippet
	}
	return snippet + "…"
}

// listSearchSnippet is canvasSearchSnippet for a list: the description prose past
// the name, windowed around the first match. An empty description yields an empty
// snippet, so a list matched by name alone shows its name and nothing else.
func listSearchSnippet(list domain.List, terms []string) string {
	text := strings.TrimSpace(strings.TrimPrefix(domain.ListSearchText(list.Name, list.DescriptionBlocks), list.Name))
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= canvasSnippetRunes {
		return text
	}
	start := 0
	if spans := matchedSpans(strings.ToLower(text), terms); len(spans) > 0 && len(strings.ToLower(text)) == len(text) {
		start = len([]rune(text[:spans[0].start])) - canvasSnippetRunes/2
	}
	if start < 0 {
		start = 0
	}
	if start+canvasSnippetRunes > len(runes) {
		start = len(runes) - canvasSnippetRunes
	}
	snippet := strings.TrimSpace(string(runes[start : start+canvasSnippetRunes]))
	if start > 0 {
		snippet = "…" + snippet
	}
	return snippet + "…"
}

// markedText is the plain-field marker: escape, then emphasise what matched.
// The result is HTML because it carries the <mark> elements, and nothing else:
// every other byte went through html.EscapeString on the way out.
func markedText(text string, terms []string) template.HTML {
	return template.HTML(markTerms(text, terms)) // #nosec G203 -- markTerms escapes everything but the <mark> tags it emits.
}

// canvasSnippetRunes is counted in runes rather than bytes so a document in a
// non-Latin script is not cut to a quarter of the length a Latin one gets.
const canvasSnippetRunes = 160

// searchPageURL builds the next-page link for a cursor-paged tab. People and
// Channels used to render every match at once because they filtered a directory
// the handler had already loaded whole; now that they ask the store a question,
// they get a page and have to offer the next one like every other tab.
// searchFilterOptionLimit bounds the from:/in: pickers. It is not a claim about
// how many members a workspace has; it is the point past which a select element
// stops being usable and the typeahead is the answer.
const searchFilterOptionLimit = 200

// searchableTerms drops the modifiers and the negations. A modifier is an
// instruction to the search rather than a word in the result, and a negated
// term is by definition absent from anything that matched.
func searchableTerms(tokens []string) []string {
	terms := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if strings.HasPrefix(token, "-") || strings.Contains(token, ":") {
			continue
		}
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			terms = append(terms, trimmed)
		}
	}
	return terms
}

func searchPageURL(r *http.Request, hasMore bool, cursor string) string {
	if !hasMore || cursor == "" {
		return ""
	}
	values := cloneURLValues(r.URL.Query())
	values.Set("cursor", cursor)
	return "/app/search?" + values.Encode()
}

func searchHistoryViews(values []domain.SearchHistoryEntry, channel string) []searchHistoryView {
	views := make([]searchHistoryView, 0, len(values))
	for _, value := range values {
		query := strings.TrimSpace(value.Query)
		if query == "" {
			continue
		}
		parameters := url.Values{"q": {query}, "channel": {channel}, "type": {"messages"}}
		views = append(views, searchHistoryView{Query: query, URL: "/app/search?" + parameters.Encode()})
	}
	return views
}

func (h Handler) searchSuggestions(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeSearchRead)
	if err != nil {
		writeJSONAuthError(w, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(query) > 500 {
		h.writeSearchSuggestions(w, http.StatusBadRequest, nil)
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	recent, err := h.Messages.RecentSearches(r.Context(), principal.WorkspaceID, principal.UserID, recentSearchWindow)
	if err != nil {
		h.writeSearchSuggestions(w, http.StatusServiceUnavailable, nil)
		return
	}
	folded := domain.FoldSearchText(query)
	items := make([]searchSuggestion, 0, 20)
	for _, view := range searchHistoryViews(recent, channel) {
		if folded != "" && !strings.Contains(domain.FoldSearchText(view.Query), folded) {
			continue
		}
		items = append(items, searchSuggestion{Kind: "recent", Label: view.Query, Description: "Recent search", URL: view.URL})
	}
	if query == "" {
		h.writeSearchSuggestions(w, http.StatusOK, items)
		return
	}
	members, conversations, err := h.searchFilterOptions(r.Context(), principal)
	if err != nil {
		h.writeSearchSuggestions(w, http.StatusServiceUnavailable, nil)
		return
	}
	for _, member := range members {
		if len(items) >= 20 {
			break
		}
		if !strings.Contains(domain.FoldSearchText(member.Name+" "+member.RealName), folded) {
			continue
		}
		items = append(items, searchSuggestion{
			Kind: "person", Label: member.Name, Description: "Person",
			URL: "/app/members?user=" + url.QueryEscape(member.ID),
		})
	}
	for _, conversation := range conversations {
		if len(items) >= 20 {
			break
		}
		if !strings.Contains(domain.FoldSearchText(conversation.Name), folded) {
			continue
		}
		items = append(items, searchSuggestion{
			Kind: "channel", Label: "# " + conversation.Name, Description: "Channel",
			URL: "/app?channel=" + url.QueryEscape(conversation.ID),
		})
	}
	fileRequest := domain.PageRequest{Limit: 100}
	for pageNumber := 0; pageNumber < 5 && len(items) < 20; pageNumber++ {
		page, fileErr := h.Messages.Files(r.Context(), principal.WorkspaceID, principal.UserID, fileRequest)
		if fileErr != nil {
			h.writeSearchSuggestions(w, http.StatusServiceUnavailable, nil)
			return
		}
		for _, file := range page.Files {
			label := file.Title
			if label == "" {
				label = file.Name
			}
			if !strings.Contains(domain.FoldSearchText(file.Name+" "+file.Title), folded) {
				continue
			}
			items = append(items, searchSuggestion{
				Kind: "file", Label: label, Description: "File",
				URL: "/api/files/" + url.PathEscape(string(file.ID)),
			})
			if len(items) >= 20 {
				break
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == fileRequest.Cursor {
			break
		}
		fileRequest.Cursor = page.NextCursor
	}
	h.writeSearchSuggestions(w, http.StatusOK, items)
}

func (h Handler) writeSearchSuggestions(w http.ResponseWriter, status int, items []searchSuggestion) {
	if items == nil {
		items = []searchSuggestion{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(searchSuggestionsResponse{Items: items})
}

func (h Handler) writeSearchError(w http.ResponseWriter, data searchData, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidSearch):
		data.Error = "Enter between one and 500 characters and use supported Slack search modifiers."
	case errors.Is(err, store.ErrInvalidArgument):
		data.Error = "Check the query and filters, then search again."
	case errors.Is(err, domain.ErrInvalidCursor):
		data.Error = "That results link is no longer valid. Search again to see matches."
	default:
		h.writeStoreError(w, err, "Search is temporarily unavailable.")
		return
	}
	data.Searched = false
	h.writeHTML(w, searchTemplate, data, http.StatusBadRequest, "search rendering unavailable")
}

// searchFilterOptions fills the from:/in: pickers, and only those. It used to
// page the entire member directory and the entire channel list into memory on
// every search request — including a Messages search that never reads either —
// because the People and Channels tabs were answered by filtering those lists
// in the handler. Those tabs ask the store now, so the exhaustive walk bought
// nothing and cost an unbounded amount of work per request on a large
// workspace. One page is what a picker can usefully show; the typeahead at
// /app/search/suggestions is how a member reaches the rest, and it always was.
func (h Handler) searchFilterOptions(ctx context.Context, principal auth.Principal) ([]memberView, []conversationView, error) {
	members := make([]memberView, 0)
	page, err := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: searchFilterOptionLimit})
	if err != nil {
		return nil, nil, err
	}
	for _, user := range page.Users {
		if user.Deleted {
			continue
		}
		name := displayName(user)
		members = append(members, memberView{ID: string(user.ID), Name: name, RealName: user.RealName, AuthorInitial: initial(name), IsSelf: user.ID == principal.UserID})
	}
	sort.Slice(members, func(left, right int) bool { return members[left].Name < members[right].Name })
	conversations, err := h.visibleChannelOptions(ctx, principal)
	if err != nil {
		return nil, nil, err
	}
	return members, conversations, nil
}

func (h Handler) visibleChannelOptions(ctx context.Context, principal auth.Principal) ([]conversationView, error) {
	conversations := make([]conversationView, 0)
	page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
		Limit: searchFilterOptionLimit, IncludeClosedDirects: true,
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	})
	if err != nil {
		return nil, err
	}
	for _, conversation := range page.Conversations {
		conversations = append(conversations, conversationView{ID: string(conversation.ID), Name: conversationName(conversation)})
	}
	sort.Slice(conversations, func(left, right int) bool { return conversations[left].Name < conversations[right].Name })
	return conversations, nil
}

func (h Handler) emojiOptions(w http.ResponseWriter, r *http.Request) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		writeJSONAuthError(w, err)
		return
	}
	if !principal.HasScope(auth.ScopeEmojiRead) && !principal.HasScope(auth.ScopeChatWrite) && !principal.HasScope(auth.ScopeReactionsWrite) {
		writeJSONAuthError(w, auth.ErrMissingScope)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	if len(query) > 100 {
		writeJSONRefusal(w, http.StatusBadRequest, "query_too_long")
		return
	}
	custom, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		writeJSONRefusal(w, http.StatusServiceUnavailable, "emoji_unavailable")
		return
	}
	recent := strings.Split(r.URL.Query().Get("recent"), ",")
	if len(recent) > 24 {
		recent = recent[:24]
	}
	options := mergedEmojiOptions(query, category, recent, custom, 60)
	categories := []string{"Recent", "Custom"}
	for _, value := range slackemoji.Categories() {
		categories = append(categories, value.Name)
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(map[string]any{"options": options, "count": len(options), "categories": categories})
}

func mergedEmojiOptions(query, category string, recent []string, custom []domain.CustomEmoji, limit int) []emojiOptionView {
	query = strings.ToLower(strings.Trim(strings.TrimSpace(query), ":"))
	category = strings.TrimSpace(category)
	images := customEmojiImages(custom)
	result := make([]emojiOptionView, 0, limit)
	seen := make(map[string]struct{}, limit)
	appendOption := func(option emojiOptionView) {
		if len(result) == limit || option.Name == "" {
			return
		}
		if _, exists := seen[option.Name]; exists {
			return
		}
		seen[option.Name] = struct{}{}
		result = append(result, option)
	}
	if category == "Recent" {
		customByName := make(map[string]domain.CustomEmoji, len(custom))
		for _, value := range custom {
			customByName[strings.ToLower(strings.TrimSpace(value.Name))] = value
		}
		for _, rawName := range recent {
			name := strings.ToLower(strings.Trim(strings.TrimSpace(rawName), ":"))
			if name == "" || (query != "" && !strings.Contains(name, query)) {
				continue
			}
			if _, exists := customByName[name]; exists {
				if imageURL := images[name]; imageURL != "" {
					appendOption(emojiOptionView{Name: name, Display: ":" + name + ":", ImageURL: imageURL, Category: "Custom", Custom: true})
				}
				continue
			}
			if value, ok := slackemoji.Lookup(name); ok {
				appendOption(emojiOptionView{Name: value.Name, Display: slackemoji.Unicode(value), Category: value.Category, SkinTones: value.SkinTones})
			}
		}
		return result
	}
	includeCustom := category == "" || category == "Custom"
	includeStandard := category != "Custom"
	for _, value := range custom {
		if !includeCustom {
			break
		}
		if len(result) == limit {
			break
		}
		name := strings.ToLower(strings.TrimSpace(value.Name))
		if name == "" || (query != "" && !strings.Contains(name, query)) {
			continue
		}
		imageURL := images[name]
		if imageURL == "" {
			continue
		}
		appendOption(emojiOptionView{
			Name: name, Display: ":" + name + ":", ImageURL: imageURL,
			Category: "Custom", Custom: true,
		})
	}
	if len(result) == limit {
		return result
	}
	if !includeStandard {
		return result
	}
	for _, value := range slackemoji.Search(query, len(slackemoji.All())) {
		if category != "" && value.Category != category {
			continue
		}
		display := slackemoji.Unicode(value)
		if display == "" {
			continue
		}
		appendOption(emojiOptionView{Name: value.Name, Display: display, Category: value.Category, SkinTones: value.SkinTones})
		if len(result) == limit {
			break
		}
	}
	return result
}

func customEmojiImages(values []domain.CustomEmoji) map[string]string {
	byName := make(map[string]domain.CustomEmoji, len(values))
	for _, value := range values {
		name := strings.ToLower(strings.TrimSpace(value.Name))
		if name != "" {
			byName[name] = value
		}
	}
	result := make(map[string]string, len(values))
	var resolve func(string, map[string]struct{}) string
	resolve = func(name string, seen map[string]struct{}) string {
		name = strings.ToLower(strings.TrimSpace(name))
		if cached, ok := result[name]; ok {
			return cached
		}
		if _, cycle := seen[name]; cycle {
			return ""
		}
		value, ok := byName[name]
		if !ok {
			return ""
		}
		seen[name] = struct{}{}
		defer delete(seen, name)
		imageURL := strings.TrimSpace(value.URL)
		if value.AliasFor != "" {
			imageURL = resolve(value.AliasFor, seen)
		}
		if !safeEmojiImageURL(imageURL) {
			imageURL = ""
		}
		result[name] = imageURL
		return imageURL
	}
	for name := range byName {
		resolve(name, make(map[string]struct{}))
	}
	return result
}

func safeEmojiImageURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func searchQueryWithFilters(query string, data searchData) string {
	values := []string{strings.TrimSpace(query)}
	if data.SelectedMember != "" {
		values = append(values, "from:"+data.SelectedMember)
	}
	if data.SelectedConversation != "" && !data.CurrentOnly {
		values = append(values, "in:"+data.SelectedConversation)
	}
	if data.After != "" {
		values = append(values, "after:"+data.After)
	}
	if data.Before != "" {
		values = append(values, "before:"+data.Before)
	}
	if data.Has != "" {
		if data.Type == "files" {
			values = append(values, "type:"+data.Has)
		} else {
			values = append(values, "has:"+data.Has)
		}
	}
	return strings.Join(values, " ")
}

func cloneURLValues(source url.Values) url.Values {
	values := make(url.Values, len(source))
	for key, entries := range source {
		values[key] = append([]string(nil), entries...)
	}
	return values
}

// newResultViews renders search results. terms are marked inside each body;
// pass none for a list that is not answering a query — Activity uses the same
// builder to show a message nobody searched for, and marking there would
// emphasise words at random.
func (h Handler) newResultViews(ctx context.Context, principal auth.Principal, messages []domain.Message, names *userNames, terms ...string) []messageView {
	views := make([]messageView, 0, len(messages))
	emojiImages := map[string]string{}
	if customEmoji, err := h.Messages.Emojis(ctx, principal.WorkspaceID, principal.UserID); err == nil {
		emojiImages = customEmojiImages(customEmoji)
	}
	for _, message := range messages {
		author := names.name(message.AuthorID)
		channelName := string(message.Conversation)
		channelPrefix := "#"
		if conversation, err := h.Messages.ConversationInfo(ctx, principal.WorkspaceID, principal.UserID, message.Conversation); err == nil {
			channelName = conversationName(conversation)
			if conversation.IsDirectOrGroup() {
				channelPrefix = ""
				if participants := h.participantNames(ctx, principal, conversation.ID); participants != "" {
					channelName = participants
				}
			}
		}
		// A search hit is opened where it lives: the window that includes the
		// message, the message anchored, and the thread pane only when the hit
		// really is a threaded reply. Message cursors are exclusive, so the hit's
		// own cursor would remove it from the descending page. A boundary one
		// nanosecond later includes the exact stored row without changing the
		// repository-wide cursor contract.
		before := ""
		boundary := message
		boundary.CreatedAt = boundary.CreatedAt.Add(time.Nanosecond)
		if cursor, err := domain.NewMessageCursor(boundary); err == nil {
			before = string(cursor)
		}
		displayMessage := message
		displayMessage.Text = resolveSlackUserMentions(message.Text, names)
		view := messageView{
			ID:            string(message.ID),
			Anchor:        messageAnchor(message.ID),
			AuthorName:    author,
			AuthorInitial: initial(author),
			Text:          message.Text,
			DisplayText:   newRichMessageContentMarking(displayMessage, nil, terms).Text,
			MachineTime:   message.CreatedAt.UTC().Format(time.RFC3339Nano),
			DisplayTime:   formatTime(message.CreatedAt),
			Channel:       string(message.Conversation),
			ChannelName:   channelName,
			ChannelPrefix: channelPrefix,
			Permalink:     appURL(string(message.Conversation), string(message.ThreadTimestamp), before, messageAnchor(message.ID), ""),
		}
		// A search hit shows its author's current status beside their name, the
		// same projection the timeline makes and only for a human author.
		if message.AppID == "" && message.AuthorID != "" {
			if emoji := names.statusEmoji(message.AuthorID); emoji != "" {
				view.AuthorStatus = renderReactionEmoji(emoji, emojiImages)
				view.AuthorStatusText = names.statusText(message.AuthorID)
			}
		}
		views = append(views, view)
	}
	return views
}

// ---------------------------------------------------------------------------
// Members and profile
// ---------------------------------------------------------------------------

func pageCSRFToken(r *http.Request) (string, error) {
	session, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(session.Value) == "" {
		return "", auth.ErrNotAuthenticated
	}
	return auth.CSRFToken(session.Value), nil
}

// canvasSections reads the canvas as the document it is. Only a markdown
// section is editable here: this client has no editor for the richer section
// types the API accepts, and offering a plain textarea for one would silently
// flatten it on save. Those sections render as stored and say they are not
// editable, which is the difference between "this client cannot edit it" and
// "this canvas cannot be edited".
func canvasSections(value domain.Canvas) ([]canvasSectionView, bool) {
	var document struct {
		Sections []domain.CanvasSection `json:"sections"`
	}
	if json.Unmarshal([]byte(value.DocumentContent), &document) != nil {
		return nil, false
	}
	views := make([]canvasSectionView, 0, len(document.Sections))
	for index, section := range document.Sections {
		views = append(views, canvasSectionView{
			ID: section.ID, Type: string(section.Type), Text: section.Text,
			Editable: section.Type.Editable(),
			Position: index + 1,
		})
	}
	return views, true
}

func canvasEditor(value domain.Canvas) (body, sectionID string, editable bool) {
	var document struct {
		Sections []domain.CanvasSection `json:"sections"`
	}
	if json.Unmarshal([]byte(value.DocumentContent), &document) != nil {
		return "", "", false
	}
	if len(document.Sections) == 0 {
		return "", "", true
	}
	if len(document.Sections) == 1 && document.Sections[0].Type == domain.CanvasSectionMarkdown {
		section := document.Sections[0]
		return section.Text, section.ID, true
	}
	parts := make([]string, 0, len(document.Sections))
	for _, section := range document.Sections {
		if text := strings.TrimSpace(section.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), "", false
}

func canvasBody(value domain.Canvas) string {
	body, _, _ := canvasEditor(value)
	return body
}

func (h Handler) canvases(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.Canvases(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 48, Cursor: cursor})
	if err != nil {
		h.writeStoreError(w, err, "Canvases are temporarily unavailable.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	cards := make([]documentCardView, 0, len(page.Canvases))
	for _, value := range page.Canvases {
		preview := canvasBody(value)
		if len([]rune(preview)) > 180 {
			preview = string([]rune(preview)[:180]) + "…"
		}
		cards = append(cards, documentCardView{ID: string(value.ID), Title: value.Title, Preview: preview, URL: "/app/canvases/" + url.PathEscape(string(value.ID)), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	more := ""
	if page.HasMore {
		more = "/app/canvases?cursor=" + url.QueryEscape(string(page.NextCursor))
	}
	h.writeHTML(w, documentsTemplate, documentsData{Kind: "canvas", Title: "Canvases", CSRFToken: csrf, CanWrite: principal.HasScope(auth.ScopeCanvasesWrite), Canvases: cards, MoreURL: more, Notice: strings.TrimSpace(r.URL.Query().Get("notice"))}, http.StatusOK, "canvas rendering unavailable")
}

func (h Handler) canvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	value, err := h.Messages.Canvas(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID"))))
	if err != nil {
		h.writeStoreError(w, err, "That canvas is not available.")
		return
	}
	access, err := h.Messages.CanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, value.ID)
	if err != nil {
		h.writeStoreError(w, err, "That canvas is not available.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	owner := value.OwnerID == principal.UserID
	canWrite := access.Access == domain.AccessWrite || access.Access == domain.AccessOwner
	sections, readable := canvasSections(value)
	canEdit := canWrite && principal.HasScope(auth.ScopeCanvasesWrite)
	readOnlyReason := ""
	if !readable {
		readOnlyReason = "This canvas could not be read as a document. It is shown as stored so nothing is lost, and editing is disabled until it can be parsed."
	}
	names := h.newUserNames(r.Context(), principal)
	comments := make([]canvasCommentView, 0, 8)
	if page, commentErr := h.Messages.CanvasComments(r.Context(), principal.WorkspaceID, principal.UserID, value.ID, domain.PageRequest{Limit: 100}); commentErr == nil {
		positions := make(map[string]int, len(sections))
		for index, section := range sections {
			positions[section.ID] = index + 1
		}
		for _, comment := range page.Comments {
			view := canvasCommentView{
				ID: string(comment.ID), AuthorName: names.name(comment.UserID), SectionID: comment.SectionID,
				Text: comment.Text, DisplayTime: formatTime(comment.CreatedAt),
				MachineTime: comment.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if position, ok := positions[comment.SectionID]; ok {
				view.SectionName = "Section " + strconv.Itoa(position)
			} else if comment.SectionID != "" {
				// The paragraph it was about has gone. Deleting a paragraph does
				// not unsay what was said about it, so the comment stays and
				// says what happened instead of pointing at nothing.
				view.SectionName = "a removed section"
			}
			if comment.UserID == principal.UserID {
				view.DeleteURL = "/app/canvases/" + url.PathEscape(string(value.ID)) + "/comments/" + url.PathEscape(string(comment.ID)) + "/delete"
			}
			comments = append(comments, view)
		}
	}
	revisions := make([]canvasRevisionView, 0, 8)
	if history, historyErr := h.Messages.CanvasRevisions(r.Context(), principal.WorkspaceID, principal.UserID, value.ID, domain.PageRequest{Limit: 10}); historyErr == nil {
		for _, revision := range history.Revisions {
			view := canvasRevisionView{
				Version: revision.Version, Title: revision.Title,
				Excerpt:     canvasRevisionExcerpt(revision),
				DisplayTime: formatTime(revision.CreatedAt), MachineTime: revision.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
			if revision.EditedBy != "" {
				view.EditorName = names.name(revision.EditedBy)
			}
			if canEdit && readable {
				view.RestoreURL = "/app/canvases/" + url.PathEscape(string(value.ID)) + "/restore"
			}
			revisions = append(revisions, view)
		}
	}
	grants, shareTargets := h.canvasSharing(r.Context(), principal, value, owner)
	h.writeHTML(w, canvasTemplate, canvasData{Comments: comments, Revisions: revisions, Grants: grants, ShareTargets: shareTargets, CanShare: owner && principal.HasScope(auth.ScopeCanvasesWrite), SharePath: "/app/canvases/" + url.PathEscape(string(value.ID)), ShareNoun: "canvas", ID: string(value.ID), Title: value.Title, Sections: sections, UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), CSRFToken: csrf, CanWrite: canEdit && readable, CanDelete: owner && principal.HasScope(auth.ScopeCanvasesWrite), ReadOnlyReason: readOnlyReason, Notice: strings.TrimSpace(r.URL.Query().Get("notice"))}, http.StatusOK, "canvas rendering unavailable")
}

// documentSharing builds the sharing list and, for the owner, the people and
// channels still worth offering. The list is for everyone who may read the
// document; the targets are only ever used by the owner's form, so they are not
// gathered for anybody else — a member who cannot grant has no use for a
// directory walk on every document they open.
func (h Handler) documentSharing(ctx context.Context, principal auth.Principal, ownerID domain.UserID, listed []documentGrant, owner bool) ([]grantView, []shareTargetView) {
	names := h.newUserNames(ctx, principal)
	grants := make([]grantView, 0, len(listed)+1)
	grants = append(grants, grantView{Name: names.name(ownerID), Access: documentAccessLabel("owner"), Reason: "created it"})
	shared := map[string]bool{"user:" + string(ownerID): true}
	for _, grant := range listed {
		view := grantView{Access: documentAccessLabel(grant.Access)}
		switch grant.EntityType {
		case domain.GrantUser:
			if domain.UserID(grant.EntityID) == ownerID {
				// The owner already leads the list; a second row for the same
				// person would read as two people with the same name.
				continue
			}
			view.Name = names.name(domain.UserID(grant.EntityID))
			view.Target = "user:" + grant.EntityID
		case domain.GrantChannel:
			view.Name = names.channelName(domain.ConversationID(grant.EntityID))
			view.Target = "channel:" + grant.EntityID
		default:
			// A channel's own canvas tab. Revoking it would leave the channel
			// with a tab pointing at a document nobody in it may open, so this
			// client shows the grant and does not offer to remove it.
			view.Name = names.channelName(domain.ConversationID(grant.EntityID))
			view.Reason = "this is the channel's canvas"
		}
		shared[string(grant.EntityType)+":"+grant.EntityID] = true
		grants = append(grants, view)
	}
	if !owner {
		return grants, nil
	}
	targets := make([]shareTargetView, 0, 16)
	if directory, dirErr := h.Messages.Users(ctx, principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: searchFilterOptionLimit}); dirErr == nil {
		for _, candidate := range directory.Users {
			if candidate.Deleted || shared["user:"+string(candidate.ID)] {
				continue
			}
			targets = append(targets, shareTargetView{Value: "user:" + string(candidate.ID), Name: displayName(candidate), Kind: "Member"})
		}
	}
	if channels, channelErr := h.visibleChannelOptions(ctx, principal); channelErr == nil {
		for _, channel := range channels {
			if shared["channel:"+channel.ID] || shared["channel_canvas:"+channel.ID] {
				continue
			}
			targets = append(targets, shareTargetView{Value: "channel:" + channel.ID, Name: "#" + channel.Name, Kind: "Channel"})
		}
	}
	return grants, targets
}

// canvasSharing reads the grants on one canvas and describes them.
func (h Handler) canvasSharing(ctx context.Context, principal auth.Principal, canvas domain.Canvas, owner bool) ([]grantView, []shareTargetView) {
	listed, err := h.Messages.CanvasGrants(ctx, principal.WorkspaceID, principal.UserID, canvas.ID)
	if err != nil {
		// Sharing is not why the canvas was opened. A canvas that reads but
		// whose grants do not is still a canvas worth showing, so the section
		// goes quiet rather than the page failing.
		return nil, nil
	}
	grants := make([]documentGrant, 0, len(listed))
	for _, grant := range listed {
		grants = append(grants, documentGrant{EntityType: grant.EntityType, EntityID: grant.EntityID, Access: grant.Access})
	}
	return h.documentSharing(ctx, principal, canvas.OwnerID, grants, owner)
}

// listSharing is the same read for a list.
func (h Handler) listSharing(ctx context.Context, principal auth.Principal, value domain.List, owner bool) ([]grantView, []shareTargetView) {
	listed, err := h.Messages.ListGrants(ctx, principal.WorkspaceID, principal.UserID, value.ID)
	if err != nil {
		return nil, nil
	}
	grants := make([]documentGrant, 0, len(listed))
	for _, grant := range listed {
		grants = append(grants, documentGrant{EntityType: grant.EntityType, EntityID: grant.EntityID, Access: grant.Access})
	}
	return h.documentSharing(ctx, principal, value.OwnerID, grants, owner)
}

// documentAccessLabel says what a grant lets someone do. The stored words are
// the API's — "read", "write", "owner" — and a sharing list is read by whoever
// is deciding what to give, not by whoever wrote the schema.
func documentAccessLabel(access domain.AccessLevel) string {
	switch access {
	case domain.AccessOwner:
		return "Owner"
	case domain.AccessWrite:
		return "Can edit"
	case domain.AccessRead:
		return "Can view"
	}
	return string(access)
}

// shareCanvas grants access to one person or one channel. The target arrives as
// a single field because the two are one choice in the form: a member picks who
// to share with, not first what kind of thing they are about to pick.
func (h Handler) shareCanvas(w http.ResponseWriter, r *http.Request) {
	h.changeCanvasSharing(w, r, true)
}

func (h Handler) revokeCanvasShare(w http.ResponseWriter, r *http.Request) {
	h.changeCanvasSharing(w, r, false)
}

// shareList and revokeListShare are the list's half of the same surface.
func (h Handler) shareList(w http.ResponseWriter, r *http.Request) {
	h.changeListSharing(w, r, true)
}

func (h Handler) revokeListShare(w http.ResponseWriter, r *http.Request) {
	h.changeListSharing(w, r, false)
}

func (h Handler) changeListSharing(w http.ResponseWriter, r *http.Request, granting bool) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	channelIDs, userIDs, chosen := parseShareTarget(fields["target"])
	if !chosen {
		h.writeMutationError(w, r, http.StatusBadRequest, "Sharing did not change", "Choose a member or a channel to share this list with.")
		return
	}
	if granting {
		err = h.Messages.SetListAccess(r.Context(), principal.WorkspaceID, principal.UserID, id, shareAccessLevel(fields["access"]), channelIDs, userIDs)
	} else {
		err = h.Messages.DeleteListAccess(r.Context(), principal.WorkspaceID, principal.UserID, id, channelIDs, userIDs)
	}
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "Sharing did not change", "Only the owner of a list can change who it is shared with.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(id)))
}

// parseShareTarget reads the one field the sharing form posts. The kind and the
// identifier travel together because they are one choice in the form: a member
// picks who to share with, not first what kind of thing they are about to pick.
func parseShareTarget(raw string) ([]domain.ConversationID, []domain.UserID, bool) {
	kind, target, split := strings.Cut(strings.TrimSpace(raw), ":")
	if !split || target == "" {
		return nil, nil, false
	}
	switch kind {
	case "user":
		return nil, []domain.UserID{domain.UserID(target)}, true
	case "channel":
		return []domain.ConversationID{domain.ConversationID(target)}, nil, true
	}
	return nil, nil, false
}

// shareAccessLevel defaults to the weaker grant. A form that lost its access
// field must not hand out editing.
func shareAccessLevel(raw string) domain.AccessLevel {
	if access := domain.AccessLevel(strings.TrimSpace(raw)); access.Valid() {
		return access
	}
	// A form that lost its access field, or carried one this build does not
	// declare, must not hand out editing.
	return domain.AccessRead
}

func (h Handler) changeCanvasSharing(w http.ResponseWriter, r *http.Request, granting bool) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the canvas and try again.")
	if !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	channelIDs, userIDs, chosen := parseShareTarget(fields["target"])
	if !chosen {
		h.writeMutationError(w, r, http.StatusBadRequest, "Sharing did not change", "Choose a member or a channel to share this canvas with.")
		return
	}
	if granting {
		err = h.Messages.SetCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, id, shareAccessLevel(fields["access"]), channelIDs, userIDs)
	} else {
		err = h.Messages.DeleteCanvasAccess(r.Context(), principal.WorkspaceID, principal.UserID, id, channelIDs, userIDs)
	}
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "Sharing did not change", "Only the owner of a canvas can change who it is shared with.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(id)))
}

// channelCanvasURL offers a conversation's own canvas to the members who could
// use it. A non-member cannot read one and cannot create one, so the link would
// only ever produce a refusal; a client without the canvas scope is in the same
// position for a different reason.
func channelCanvasURL(principal auth.Principal, conversation domain.Conversation, isMember bool) string {
	if !isMember || conversation.ID == "" || !principal.HasScope(auth.ScopeCanvasesRead) {
		return ""
	}
	return "/app/channel-canvas?channel=" + url.QueryEscape(string(conversation.ID))
}

// channelCanvas opens the conversation's own canvas, or says there is not one
// yet. It does not create one: a canvas appearing because somebody followed a
// link would make the channel's history say an edit happened when nobody wrote
// anything, so creating stays a thing a member does on purpose.
func (h Handler) channelCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err != nil {
		h.writeStoreError(w, err, "That conversation is not available.")
		return
	}
	canvas, err := h.Messages.ConversationCanvas(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if err == nil {
		http.Redirect(w, r, "/app/canvases/"+url.PathEscape(string(canvas.ID)), http.StatusSeeOther)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		h.writeStoreError(w, err, "That conversation's canvas is not available.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	h.writeHTML(w, channelCanvasTemplate, channelCanvasData{
		Channel: string(channel), ChannelName: conversationName(conversation), CSRFToken: csrf,
		CanCreate: principal.HasScope(auth.ScopeCanvasesWrite),
		Notice:    strings.TrimSpace(r.URL.Query().Get("notice")),
	}, http.StatusOK, "canvas rendering unavailable")
}

// createChannelCanvas makes the one canvas a conversation may have. A second
// attempt is not an error the member caused: somebody else made it first, so
// they are sent to the canvas that now exists rather than told they failed.
func (h Handler) createChannelCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	canvas, err := h.Messages.CreateConversationCanvas(r.Context(), principal.WorkspaceID, principal.UserID, channel, strings.TrimSpace(fields["title"]), "")
	if err != nil {
		if existing, lookupErr := h.Messages.ConversationCanvas(r.Context(), principal.WorkspaceID, principal.UserID, channel); lookupErr == nil {
			h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(existing.ID)))
			return
		}
		h.writeMutationError(w, r, http.StatusBadRequest, "The canvas was not created", "Join the conversation and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(canvas.ID)))
}

// canvasRevisionExcerpt is the opening of what the canvas said, bounded. A
// history list is for recognising the moment you want, not for reading the
// document twice.
func canvasRevisionExcerpt(revision domain.CanvasRevision) string {
	text := strings.TrimSpace(strings.TrimPrefix(domain.CanvasSearchText(revision.Title, revision.DocumentContent), revision.Title))
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > canvasSnippetRunes {
		return strings.TrimSpace(string(runes[:canvasSnippetRunes])) + "…"
	}
	return text
}

func (h Handler) commentOnCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the canvas and try again.")
	if !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	if _, err := h.Messages.CommentOnCanvas(r.Context(), principal.WorkspaceID, principal.UserID, id, fields["section_id"], fields["text"]); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The comment was not added", "Write something to say, and check you can still open this canvas.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(id)))
}

func (h Handler) deleteCanvasComment(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the canvas and try again."); !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	if err := h.Messages.DeleteCanvasComment(r.Context(), principal.WorkspaceID, principal.UserID, domain.CanvasCommentID(strings.TrimSpace(r.PathValue("commentID")))); err != nil {
		// A comment belongs to whoever wrote it, and someone else's comment
		// answers exactly as a missing one does, so this cannot be used to
		// learn that a comment exists.
		h.writeMutationError(w, r, http.StatusNotFound, "The comment was not deleted", "It is no longer there, or it is not yours to delete.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(id)))
}

// restoreCanvas puts an earlier revision back as an ordinary edit, so the
// content it replaced becomes a revision of its own and a wrong restore is
// itself undoable.
func (h Handler) restoreCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the canvas and try again.")
	if !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	version, convErr := strconv.ParseInt(strings.TrimSpace(fields["version"]), 10, 64)
	if convErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The canvas was not restored", "Choose a revision from the history and try again.")
		return
	}
	if _, err := h.Messages.RestoreCanvasRevision(r.Context(), principal.WorkspaceID, principal.UserID, id, version); err != nil {
		h.writeMutationError(w, r, http.StatusConflict, "The canvas was not restored", "It changed elsewhere, or that revision is no longer kept. Reload the canvas and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(id))+"?notice="+url.QueryEscape("Canvas restored"))
}

func (h Handler) createCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Canvases and try again.")
	if !ok {
		return
	}
	content, _ := json.Marshal(map[string]string{"type": "markdown", "markdown": fields["body"]})
	value, err := h.Messages.CreateCanvas(r.Context(), principal.WorkspaceID, principal.UserID, fields["title"], string(content), "")
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The canvas was not created", "Enter a name and valid content, then try again.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(value.ID)))
}

func (h Handler) updateCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the canvas and try again.")
	if !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	current, err := h.Messages.Canvas(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeMutationError(w, r, http.StatusNotFound, "The canvas was not saved", "It no longer exists or you no longer have access.")
		return
	}
	sections, readable := canvasSections(current)
	if !readable {
		h.writeMutationError(w, r, http.StatusConflict, "The canvas was not saved", "Its document could not be read. Reload it before editing.")
		return
	}
	// The section being edited must still be there, still be the same section,
	// and still be one this client can edit. A canvas whose structure changed
	// under the editor is a conflict, not a silent overwrite of whatever now
	// occupies that position.
	sectionID := strings.TrimSpace(fields["section_id"])
	operation := "replace"
	if sectionID == "" {
		if len(sections) != 0 {
			h.writeMutationError(w, r, http.StatusConflict, "The canvas was not saved", "Its document structure changed. Reload it before editing.")
			return
		}
		operation = "insert_at_end"
	} else {
		known := false
		for _, section := range sections {
			if section.ID == sectionID && section.Editable {
				known = true
				break
			}
		}
		if !known {
			h.writeMutationError(w, r, http.StatusConflict, "The canvas was not saved", "That section is no longer part of this canvas, or is not one this client can edit. Reload it before editing.")
			return
		}
	}
	changes, _ := json.Marshal([]map[string]any{
		{"operation": "replace", "title_content": map[string]string{"title": strings.TrimSpace(fields["title"])}},
		{"operation": operation, "section_id": sectionID, "document_content": map[string]string{"type": "markdown", "markdown": fields["body"]}},
	})
	if err := h.Messages.EditCanvas(r.Context(), principal.WorkspaceID, principal.UserID, id, string(changes)); err != nil {
		h.writeMutationError(w, r, http.StatusConflict, "The canvas was not saved", "It changed elsewhere or you no longer have edit access. Reload it and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases/"+url.PathEscape(string(id))+"?notice=Canvas+saved")
}

func (h Handler) deleteCanvas(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeCanvasesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the canvas and try again."); !ok {
		return
	}
	id := domain.CanvasID(strings.TrimSpace(r.PathValue("canvasID")))
	if err := h.Messages.DeleteCanvas(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		h.writeMutationError(w, r, http.StatusNotFound, "The canvas was not deleted", "It no longer exists or you are not its owner.")
		return
	}
	h.redirectMutation(w, r, "/app/canvases?notice=Canvas+deleted")
}

// listCellViews lays an item's cells out under the declared columns, in the
// declared order. A column the item has no value for shows as empty rather than
// being skipped, so a row lines up with the one above it.
func listCellViews(columns []domain.ListColumn, fields string) []listCellView {
	cells, err := domain.ParseListFields(fields)
	if err != nil {
		return nil
	}
	values := make(map[string]string, len(cells))
	for _, cell := range cells {
		values[strings.TrimSpace(cell.ColumnID)] = strings.TrimSpace(domain.ListCellText(cell.Value))
	}
	views := make([]listCellView, 0, len(columns))
	for _, column := range columns {
		views = append(views, listCellView{ColumnName: column.Name, Value: values[column.Key]})
	}
	return views
}

// listItemTitle is what an item is called when the row is not drawn as cells.
//
// It used to look only for a cell under "title", which is the column this
// client substitutes for a list created without a schema. A list that declared
// its own primary column under any other name — which is every list built
// through AddListColumn or through the API — rendered every item blank as soon
// as the row was not shown as cells, so the primary column is now what is
// asked for first and "title" is the fallback it always was.
func listItemTitle(columns []domain.ListColumn, fields string) string {
	cells, err := domain.ParseListFields(fields)
	if err != nil {
		return ""
	}
	wanted := "title"
	for _, column := range columns {
		if column.Primary {
			wanted = column.Key
			break
		}
	}
	named := ""
	for _, cell := range cells {
		id := strings.TrimSpace(cell.ColumnID)
		if id == wanted {
			return strings.TrimSpace(domain.ListCellText(cell.Value))
		}
		if id == "title" && named == "" {
			named = strings.TrimSpace(domain.ListCellText(cell.Value))
		}
	}
	return named
}

func listTitleFields(title string) string {
	value, _ := json.Marshal([]map[string]any{{"column_id": "title", "value": strings.TrimSpace(title)}})
	return string(value)
}

func (h Handler) lists(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	page, err := h.Messages.Lists(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 48, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))})
	if err != nil {
		h.writeStoreError(w, err, "Lists are temporarily unavailable.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	cards := make([]documentCardView, 0, len(page.Lists))
	for _, value := range page.Lists {
		preview := "Structured list"
		if value.TodoMode {
			preview = "To-do list"
		}
		cards = append(cards, documentCardView{ID: string(value.ID), Title: value.Name, Preview: preview, URL: "/app/lists/" + url.PathEscape(string(value.ID)), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	more := ""
	if page.HasMore {
		more = "/app/lists?cursor=" + url.QueryEscape(string(page.NextCursor))
	}
	h.writeHTML(w, documentsTemplate, documentsData{Kind: "list", Title: "Lists", CSRFToken: csrf, CanWrite: principal.HasScope(auth.ScopeListsWrite), Lists: cards, MoreURL: more, Notice: strings.TrimSpace(r.URL.Query().Get("notice"))}, http.StatusOK, "list rendering unavailable")
}

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	value, err := h.Messages.List(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeStoreError(w, err, "That list is not available.")
		return
	}
	access, err := h.Messages.ListAccess(r.Context(), principal.WorkspaceID, principal.UserID, id)
	if err != nil {
		h.writeStoreError(w, err, "That list is not available.")
		return
	}
	csrf, err := pageCSRFToken(r)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	canWrite := (access.Access == domain.AccessWrite || access.Access == domain.AccessOwner) && principal.HasScope(auth.ScopeListsWrite)
	names := h.newUserNames(r.Context(), principal)
	// The picker offers only members who can open this list, because assigning
	// to anyone else is refused by the service — a control that always fails is
	// worse than no control.
	assignable := make([]memberView, 0, 8)
	if directory, dirErr := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: searchFilterOptionLimit}); dirErr == nil {
		for _, candidate := range directory.Users {
			if candidate.Deleted {
				continue
			}
			if err := h.Messages.ListAccessFor(r.Context(), principal.WorkspaceID, candidate.ID, id); err != nil {
				continue
			}
			assignable = append(assignable, memberView{ID: string(candidate.ID), Name: displayName(candidate)})
		}
	}
	columns, schemaErr := domain.ParseListSchema(value.Schema)
	if schemaErr != nil {
		// A schema this build cannot read must not hide the list. The items are
		// still there and still readable; the structure is what is missing, and
		// saying so beats an error page over a list somebody needs.
		columns = nil
	}
	structured := domain.ListSchemaIsStructured(columns)
	columnViews := make([]listColumnView, 0, len(columns))
	if structured {
		for _, column := range columns {
			columnViews = append(columnViews, listColumnView{Key: column.Key, Name: column.Name, Type: string(column.Type), Primary: column.Primary, Options: column.Options})
		}
	}
	// Resolve the layout and any filter. Views beyond the default list need
	// structure: a board needs a column whose values a lane can stand for, and a
	// table needs declared columns to head. A filter narrows the whole list to the
	// rows whose value under one column matches, and composes with every layout —
	// so it survives a switch between them and rides in each view's own links.
	groupable := listGroupableColumns(columns)
	listPath := "/app/lists/" + url.PathEscape(string(id))
	requestedView := strings.TrimSpace(r.URL.Query().Get("view"))
	board := requestedView == "board" && len(groupable) > 0
	table := requestedView == "table" && structured && len(columnViews) > 0
	dateColumns := listDateColumns(columns)
	calendar := requestedView == "calendar" && len(dateColumns) > 0
	filterColumn, filterValue, filterActive := parseListFilter(strings.TrimSpace(r.URL.Query().Get("filter")), columns)
	activeFilter := ""
	if filterActive {
		activeFilter = filterColumn.Key + ":" + filterValue
	}
	filterParams := func() url.Values {
		params := url.Values{}
		if activeFilter != "" {
			params.Set("filter", activeFilter)
		}
		return params
	}
	group := domain.ListColumn{}
	if board {
		requested := strings.TrimSpace(r.URL.Query().Get("group"))
		group = groupable[0]
		for _, candidate := range groupable {
			if candidate.Key == requested {
				group = candidate
				break
			}
		}
	}
	sortColumn := domain.ListColumn{}
	sortDesc := false
	if table {
		sortColumn = listSortColumn(columns, strings.TrimSpace(r.URL.Query().Get("sort")))
		sortDesc = strings.TrimSpace(r.URL.Query().Get("dir")) == "desc"
	}
	dateColumn := domain.ListColumn{}
	calendarMonth := time.Time{}
	if calendar {
		requested := strings.TrimSpace(r.URL.Query().Get("date"))
		dateColumn = dateColumns[0]
		for _, candidate := range dateColumns {
			if candidate.Key == requested {
				dateColumn = candidate
				break
			}
		}
		calendarMonth = calendarMonthStart(r.URL.Query().Get("month"), time.Now())
	}
	// A board, a table, a calendar, or a filter reads the whole list — its lanes,
	// its sort, its month grid, and its filter all need every item; a plain
	// unfiltered list keeps its one cursor page and its "more" link.
	var rawItems []domain.ListItem
	var more string
	var truncated bool
	if board || table || calendar || filterActive {
		rawItems, truncated, err = h.loadAllListItems(r.Context(), principal.WorkspaceID, principal.UserID, id)
	} else {
		var page domain.ListItemPage
		if page, err = h.Messages.ListItems(r.Context(), principal.WorkspaceID, principal.UserID, id, domain.PageRequest{Limit: 100, Cursor: domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))}, true); err == nil {
			rawItems = page.Items
			if page.HasMore {
				more = listPath + "?cursor=" + url.QueryEscape(string(page.NextCursor))
			}
		}
	}
	if err != nil {
		h.writeStoreError(w, err, "The list items are temporarily unavailable.")
		return
	}
	if filterActive {
		matching := make([]domain.ListItem, 0, len(rawItems))
		for _, item := range rawItems {
			if listItemGroupValue(item.Fields, filterColumn.Key) == filterValue {
				matching = append(matching, item)
			}
		}
		rawItems = matching
	}
	items := make([]listItemView, 0, len(rawItems))
	groupValues := make([]string, 0, len(rawItems))
	sortValues := make([]string, 0, len(rawItems))
	dateValues := make([]string, 0, len(rawItems))
	for _, item := range rawItems {
		row := listItemView{ID: string(item.ID), Title: listItemTitle(columns, item.Fields), Archived: item.Archived, AssigneeID: string(item.AssigneeID), ListID: string(id), CanWrite: canWrite, CSRFToken: csrf, Members: assignable}
		if structured {
			row.Cells = listCellViews(columns, item.Fields)
		}
		if item.AssigneeID != "" {
			row.AssigneeName = names.name(item.AssigneeID)
		}
		if !item.DueAt.IsZero() {
			row.DueDate = item.DueAt.UTC().Format("2006-01-02")
			row.Overdue = item.Overdue(time.Now())
		}
		items = append(items, row)
		if board {
			groupValues = append(groupValues, listItemGroupValue(item.Fields, group.Key))
		}
		if table {
			sortValues = append(sortValues, listItemGroupValue(item.Fields, sortColumn.Key))
		}
		if calendar {
			dateValues = append(dateValues, listItemGroupValue(item.Fields, dateColumn.Key))
		}
	}
	viewName := "list"
	boardViewURL := ""
	if len(groupable) > 0 {
		params := filterParams()
		params.Set("view", "board")
		boardViewURL = listURL(listPath, params)
	}
	tableViewURL := ""
	if structured && len(columnViews) > 0 {
		params := filterParams()
		params.Set("view", "table")
		tableViewURL = listURL(listPath, params)
	}
	calendarViewURL := ""
	if len(dateColumns) > 0 {
		params := filterParams()
		params.Set("view", "calendar")
		calendarViewURL = listURL(listPath, params)
	}
	var lanes []listLaneView
	var groupChoices []groupChoiceView
	groupName := ""
	if board {
		viewName = "board"
		lanes = buildListLanes(group, items, groupValues)
		groupName = group.Name
		for _, candidate := range groupable {
			params := filterParams()
			params.Set("view", "board")
			params.Set("group", candidate.Key)
			groupChoices = append(groupChoices, groupChoiceView{Name: candidate.Name, URL: listURL(listPath, params), Selected: candidate.Key == group.Key})
		}
	}
	var tableHeaders []tableHeaderView
	sortKey := ""
	sortDir := "asc"
	if table {
		viewName = "table"
		sortListItemsByColumn(items, sortValues, sortColumn, sortDesc)
		sortKey = sortColumn.Key
		if sortDesc {
			sortDir = "desc"
		}
		tableHeaders = buildTableHeaders(columns, listPath, sortKey, sortDesc, activeFilter)
	}
	var calendarWeeks [][]calendarDayView
	var dateChoices []groupChoiceView
	calendarMonthLabel, dateColumnName, prevMonthURL, nextMonthURL := "", "", "", ""
	if calendar {
		viewName = "calendar"
		calendarWeeks = buildCalendarWeeks(calendarMonth, time.Now(), items, dateValues)
		calendarMonthLabel = calendarMonth.Format("January 2006")
		dateColumnName = dateColumn.Name
		prev := filterParams()
		prev.Set("view", "calendar")
		prev.Set("date", dateColumn.Key)
		prev.Set("month", calendarMonth.AddDate(0, -1, 0).Format("2006-01"))
		prevMonthURL = listURL(listPath, prev)
		next := filterParams()
		next.Set("view", "calendar")
		next.Set("date", dateColumn.Key)
		next.Set("month", calendarMonth.AddDate(0, 1, 0).Format("2006-01"))
		nextMonthURL = listURL(listPath, next)
		for _, candidate := range dateColumns {
			params := filterParams()
			params.Set("view", "calendar")
			params.Set("date", candidate.Key)
			dateChoices = append(dateChoices, groupChoiceView{Name: candidate.Name, URL: listURL(listPath, params), Selected: candidate.Key == dateColumn.Key})
		}
	}
	// The clear link returns to the same layout without the filter, so clearing a
	// filter does not also throw away the board grouping, the table sort, or the
	// calendar month.
	clearParams := url.Values{}
	if board {
		clearParams.Set("view", "board")
		clearParams.Set("group", group.Key)
	}
	if calendar {
		clearParams.Set("view", "calendar")
		clearParams.Set("date", dateColumn.Key)
		clearParams.Set("month", calendarMonth.Format("2006-01"))
	}
	if table {
		clearParams.Set("view", "table")
		clearParams.Set("sort", sortKey)
		clearParams.Set("dir", sortDir)
	}
	owner := value.OwnerID == principal.UserID
	grants, shareTargets := h.listSharing(r.Context(), principal, value, owner)
	h.writeHTML(w, listTemplate, listData{Grants: grants, ShareTargets: shareTargets, CanShare: owner && principal.HasScope(auth.ScopeListsWrite), SharePath: listPath, ShareNoun: "list", ID: string(id), Name: value.Name, Members: assignable, Columns: columnViews, TodoMode: value.TodoMode, Items: items, MoreURL: more, CSRFToken: csrf, CanWrite: canWrite, Notice: strings.TrimSpace(r.URL.Query().Get("notice")), View: viewName, BoardActive: board, ListViewURL: listURL(listPath, filterParams()), BoardViewURL: boardViewURL, GroupChoices: groupChoices, GroupName: groupName, Lanes: lanes, BoardTruncated: truncated, TableActive: table, TableViewURL: tableViewURL, TableHeaders: tableHeaders, SortKey: sortKey, SortDir: sortDir, GroupKey: group.Key, FilterOptions: buildFilterOptions(columns, activeFilter), FilterActive: filterActive, ClearFilterURL: listURL(listPath, clearParams), CalendarActive: calendar, CalendarViewURL: calendarViewURL, CalendarWeeks: calendarWeeks, MonthLabel: calendarMonthLabel, PrevMonthURL: prevMonthURL, NextMonthURL: nextMonthURL, DateChoices: dateChoices, DateColumnName: dateColumnName}, http.StatusOK, "list rendering unavailable")
}

func (h Handler) createList(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload Lists and try again.")
	if !ok {
		return
	}
	value, err := h.Messages.CreateList(r.Context(), principal.WorkspaceID, principal.UserID, fields["title"], "[]", "", "", false, fields["todo_mode"] == "true")
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The list was not created", "Enter a name and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(value.ID)))
}

// addListColumn declares a column from the list page. Options are comma
// separated because a select's options are a short list of short words, and a
// control that made a member add them one at a time would be a worse answer to
// a smaller problem.
func (h Handler) addListColumn(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	options := make([]string, 0, 4)
	for _, option := range strings.Split(fields["options"], ",") {
		if trimmed := strings.TrimSpace(option); trimmed != "" {
			options = append(options, trimmed)
		}
	}
	if _, err := h.Messages.AddListColumn(r.Context(), principal.WorkspaceID, principal.UserID, id, fields["name"], domain.ListColumnType(strings.TrimSpace(fields["type"])), options); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The column was not added", "Give the column a name, and list at least one option for a Select column.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(id)))
}

// removeListColumn drops a column and every value recorded under it. Adding a
// column cannot invalidate anything; removing one deletes data, so the control
// says what goes and this is a separate route rather than a mode of the other.
func (h Handler) removeListColumn(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	if _, err := h.Messages.RemoveListColumn(r.Context(), principal.WorkspaceID, principal.UserID, id, fields["key"]); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The column was not removed", "The first column names the item and stays, and a column that is already gone cannot be removed again.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(id)))
}

func (h Handler) createListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	if _, err := h.Messages.CreateListItem(r.Context(), principal.WorkspaceID, principal.UserID, id, "", listTitleFields(fields["title"])); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The item was not added", "Enter a title and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(id)))
}

// assignListItem records who an item is for and when it is wanted. A due date
// is a date rather than an instant, because "due Tuesday" is what a member
// means; it is read as the end of that day in UTC so an item is not late the
// moment the day begins somewhere.
// deleteListItem removes an item for good. Completing an item hides it and can
// be undone; this cannot, which is why it is a separate control that says so
// rather than a second meaning for the same button.
func (h Handler) deleteListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "Reload the list and try again."); !ok {
		return
	}
	id := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	item := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	if err := h.Messages.DeleteListItems(r.Context(), principal.WorkspaceID, principal.UserID, id, []domain.ListItemID{item}); err != nil {
		// A member who cannot see the item is answered exactly as a member
		// looking at one somebody else has already deleted: the two are the
		// same fact from here, and distinguishing them would say whether an
		// item exists to somebody who may not know.
		h.writeMutationError(w, r, http.StatusNotFound, "The item was not deleted", "It is no longer there, or this list is not yours to change.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(id)))
}

func (h Handler) assignListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	var due time.Time
	if raw := strings.TrimSpace(fields["due"]); raw != "" {
		parsed, parseErr := time.Parse("2006-01-02", raw)
		if parseErr != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "The assignment was not saved", "Enter a due date as a calendar date, or leave it empty.")
			return
		}
		due = parsed.UTC().Add(24*time.Hour - time.Nanosecond)
	}
	if _, err := h.Messages.AssignListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, domain.UserID(strings.TrimSpace(fields["assignee"])), due); err != nil {
		h.writeMutationError(w, r, http.StatusConflict, "The assignment was not saved", "The item changed elsewhere, or the person cannot open this list. Reload the list and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(listID)))
}

func (h Handler) toggleListItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeListsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the list and try again.")
	if !ok {
		return
	}
	listID := domain.ListID(strings.TrimSpace(r.PathValue("listID")))
	itemID := domain.ListItemID(strings.TrimSpace(r.PathValue("itemID")))
	archived := fields["archived"] == "true"
	if _, err := h.Messages.UpdateListItem(r.Context(), principal.WorkspaceID, principal.UserID, listID, itemID, "", archived); err != nil {
		h.writeMutationError(w, r, http.StatusConflict, "The item was not updated", "It changed elsewhere or you no longer have edit access. Reload the list and try again.")
		return
	}
	h.redirectMutation(w, r, "/app/lists/"+url.PathEscape(string(listID)))
}

func (h Handler) members(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	h.renderMembers(w, r, principal, nil, nil, "", http.StatusOK)
}

func (h Handler) directMessages(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	foldedQuery := domain.FoldSearchText(query)
	users, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: 100})
	if err != nil {
		h.writeStoreError(w, err, "Direct messages are temporarily unavailable.")
		return
	}
	members := make([]memberView, 0, len(users.Users))
	for _, user := range users.Users {
		if user.Deleted || user.ID == principal.UserID {
			continue
		}
		name := displayName(user)
		if foldedQuery != "" && !strings.Contains(domain.FoldSearchText(name+" "+user.RealName+" "+user.Email), foldedQuery) {
			continue
		}
		members = append(members, memberView{ID: string(user.ID), Name: name, RealName: user.RealName, AuthorInitial: initial(name)})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

	recentPage, err := h.Messages.Conversations(r.Context(), principal.WorkspaceID, principal.UserID, domain.ConversationListRequest{
		Limit:                100,
		Types:                []domain.ConversationType{domain.ConversationTypeIM, domain.ConversationTypeMPIM},
		IncludeClosedDirects: foldedQuery != "",
	})
	if err != nil {
		h.writeStoreError(w, err, "Recent direct messages are temporarily unavailable.")
		return
	}
	recent := make([]conversationView, 0, len(recentPage.Conversations))
	for _, conversation := range recentPage.Conversations {
		name := conversation.Name
		memberPage, memberErr := h.Messages.ConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: 10})
		if memberErr != nil {
			continue
		}
		names := make([]string, 0, len(memberPage.Users))
		ids := make([]string, 0, len(memberPage.Users))
		for _, user := range memberPage.Users {
			if user.ID == principal.UserID {
				continue
			}
			names = append(names, displayName(user))
			ids = append(ids, string(user.ID))
		}
		sort.Strings(names)
		sort.Strings(ids)
		if name == "" || name == "direct" {
			name = strings.Join(names, ", ")
		}
		if foldedQuery != "" && !strings.Contains(domain.FoldSearchText(name), foldedQuery) {
			continue
		}
		item := conversationView{ID: string(conversation.ID), Name: name, UnreadCount: conversation.UnreadCount, IsGroupDirect: conversation.Kind == domain.ConversationTypeMPIM, OpenUsers: strings.Join(ids, ",")}
		if history, historyErr := h.Messages.History(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID, domain.PageRequest{Limit: 1, Descending: true}); historyErr == nil && len(history.Messages) == 1 {
			item.RecentAt = history.Messages[0].CreatedAt
		}
		recent = append(recent, item)
	}
	sort.Slice(recent, func(i, j int) bool {
		if !recent[i].RecentAt.Equal(recent[j].RecentAt) {
			return recent[i].RecentAt.After(recent[j].RecentAt)
		}
		return recent[i].Name < recent[j].Name
	})
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	h.writeHTML(w, directMessagesTemplate, directMessagesData{
		Query: query, Recent: recent, Members: members,
		CSRFToken:  auth.CSRFToken(sessionCookie.Value),
		CanMessage: principal.HasScope(auth.ScopeChannelsManage),
	}, http.StatusOK, "direct message rendering unavailable")
}

func (h Handler) renderMembers(w http.ResponseWriter, r *http.Request, principal auth.Principal, submitted *domain.UserProfile, submittedScheduled *scheduledStatusView, message string, status int) {
	cursor := domain.Cursor(strings.TrimSpace(r.URL.Query().Get("cursor")))
	page, err := h.Messages.Users(r.Context(), principal.WorkspaceID, principal.UserID, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCursor) {
			h.writePageError(w, http.StatusBadRequest, "That members link is not valid", "Open the member directory again to see who is here.")
			return
		}
		h.writeStoreError(w, err, "The member directory is temporarily unavailable.")
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your profile is temporarily unavailable.")
		return
	}
	scheduledStatuses, err := h.Messages.ScheduledUserStatuses(r.Context(), principal.WorkspaceID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your scheduled statuses are temporarily unavailable.")
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	// A member's status emoji is a shortcode; resolve it to a glyph the same way
	// the timeline resolves reactions, so the directory shows an emoji rather
	// than raw colons. Custom emoji fall back to their shortcode when unavailable.
	emojiImages := map[string]string{}
	if customEmoji, err := h.Messages.Emojis(r.Context(), principal.WorkspaceID, principal.UserID); err == nil {
		emojiImages = customEmojiImages(customEmoji)
	}
	// The viewer's VIPs decide which directory rows offer "Remove VIP" instead of
	// "Mark as VIP". A read failure leaves the toggles at "Mark", which is safe.
	vips := map[domain.UserID]struct{}{}
	if preferences, prefErr := h.Messages.WorkspaceNotificationPreferences(r.Context(), principal.WorkspaceID, principal.UserID); prefErr == nil {
		for _, id := range preferences.VIPs {
			vips[id] = struct{}{}
		}
	}
	members := make([]memberView, 0, len(page.Users))
	for _, user := range page.Users {
		// A deactivated account is not a person to message: UserInfo already
		// treats it as absent, and offering "Message <them>" here opened a dead
		// conversation or answered with a bare 404.
		if user.Deleted {
			continue
		}
		name := displayName(user)
		isSelf := user.ID == principal.UserID
		_, isVIP := vips[user.ID]
		members = append(members, memberView{ID: string(user.ID), Name: name, RealName: user.RealName, Profile: user.Profile, StatusDisplay: statusEmojiDisplay(user.Profile.StatusEmoji, emojiImages), Presence: webPresence(user.Presence, isSelf), AvatarURL: profileImageURL(user.Profile), AuthorInitial: initial(name), IsSelf: isSelf, IsVIP: isVIP})
	}
	profile := current.Profile
	if submitted != nil {
		profile = *submitted
	}
	data := membersData{
		Members:        members,
		Profile:        profile,
		StatusDisplay:  statusEmojiDisplay(profile.StatusEmoji, emojiImages),
		Presence:       current.Presence.CurrentAt(current.LastActiveAt, time.Now().UTC()),
		StatusExpires:  webUnixSeconds(profile.StatusExpiration),
		AvatarURL:      profileImageURL(profile),
		UserInitial:    initial(displayName(current)),
		CSRFToken:      auth.CSRFToken(sessionCookie.Value),
		Error:          message,
		CanEditProfile: principal.HasScope(auth.ScopeUsersWrite),
		CanMessage:     principal.HasScope(auth.ScopeChannelsManage),
		Scheduled:      make([]scheduledStatusView, 0, len(scheduledStatuses)),
	}
	for _, value := range scheduledStatuses {
		data.Scheduled = append(data.Scheduled, scheduledStatusView{
			ID: string(value.ID), StatusText: value.StatusText, StatusEmoji: value.StatusEmoji,
			StartsAt: value.StartsAt.Unix(), EndsAt: value.EndsAt.Unix(),
		})
	}
	if submittedScheduled != nil {
		if submittedScheduled.ID == "" {
			data.DraftScheduled = *submittedScheduled
		} else {
			for index := range data.Scheduled {
				if data.Scheduled[index].ID == submittedScheduled.ID {
					data.Scheduled[index] = *submittedScheduled
					break
				}
			}
		}
	}
	if page.HasMore && page.NextCursor != "" {
		data.MoreMembersURL = "/app/members?" + url.Values{"cursor": {string(page.NextCursor)}}.Encode()
	}
	h.writeHTML(w, membersTemplate, data, status, "member rendering unavailable")
}

func (h Handler) setProfile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your profile could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	current, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your profile is temporarily unavailable.")
		return
	}
	profile := current.Profile
	profile.DisplayName = fields["display_name"]
	profile.StatusText = fields["status_text"]
	profile.StatusEmoji = fields["status_emoji"]
	if fields["clear_status"] != "" {
		profile.StatusText = ""
		profile.StatusEmoji = ""
		profile.StatusExpiration = time.Time{}
	} else {
		expiration := strings.TrimSpace(fields["status_expiration"])
		if expiration == "" || expiration == "0" {
			profile.StatusExpiration = time.Time{}
		} else {
			seconds, parseErr := strconv.ParseInt(expiration, 10, 64)
			if parseErr != nil || seconds <= time.Now().UTC().Unix() {
				h.renderMembers(w, r, principal, &profile, nil, "Your profile was not saved. Choose a future time for the status to clear.", http.StatusBadRequest)
				return
			}
			profile.StatusExpiration = time.Unix(seconds, 0).UTC()
		}
	}
	avatarURL := strings.TrimSpace(fields["avatar_url"])
	if avatarURL != profileImageURL(current.Profile) {
		profile.Image24 = avatarURL
		profile.Image32 = avatarURL
		profile.Image48 = avatarURL
		profile.Image72 = avatarURL
		profile.Image192 = avatarURL
		profile.Image512 = avatarURL
		profile.Image1024 = avatarURL
	}
	if _, err := h.Messages.SetUserProfile(r.Context(), principal.WorkspaceID, principal.UserID, profile); err != nil {
		// A rejected save keeps every submitted value and says which limit it
		// crossed, instead of answering with a bare status line.
		if errors.Is(err, service.ErrInvalidProfile) {
			h.renderMembers(w, r, principal, &profile, nil, "Your profile was not saved. A display name is at most 80 characters, a status at most 100, the status emoji must be a workspace emoji of at most 64 characters, and the profile photo URL at most 2048.", http.StatusBadRequest)
			return
		}
		h.renderMembers(w, r, principal, &profile, nil, "Your profile could not be saved because the workspace store is temporarily unavailable. Try again.", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

func scheduledStatusTimes(fields map[string]string) (time.Time, time.Time, error) {
	startSeconds, startErr := strconv.ParseInt(strings.TrimSpace(fields["starts_at"]), 10, 64)
	endSeconds, endErr := strconv.ParseInt(strings.TrimSpace(fields["ends_at"]), 10, 64)
	if startErr != nil || endErr != nil || startSeconds <= 0 || endSeconds <= 0 {
		return time.Time{}, time.Time{}, service.ErrInvalidScheduledStatus
	}
	return time.Unix(startSeconds, 0).UTC(), time.Unix(endSeconds, 0).UTC(), nil
}

func scheduledStatusSubmission(fields map[string]string) scheduledStatusView {
	value := scheduledStatusView{
		ID: strings.TrimSpace(fields["id"]), StatusText: fields["status_text"], StatusEmoji: fields["status_emoji"],
	}
	value.StartsAt, _ = strconv.ParseInt(strings.TrimSpace(fields["starts_at"]), 10, 64)
	value.EndsAt, _ = strconv.ParseInt(strings.TrimSpace(fields["ends_at"]), 10, 64)
	return value
}

func (h Handler) scheduleStatus(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The scheduled status could not be read. Reload the page and try again.")
	if !ok {
		return
	}
	submitted := scheduledStatusSubmission(fields)
	startsAt, endsAt, err := scheduledStatusTimes(fields)
	if err == nil {
		_, err = h.Messages.ScheduleUserStatus(r.Context(), principal.WorkspaceID, principal.UserID, fields["status_text"], fields["status_emoji"], startsAt, endsAt)
	}
	if errors.Is(err, service.ErrInvalidScheduledStatus) {
		h.renderMembers(w, r, principal, nil, &submitted, "The status was not scheduled. Choose a future start and a later end, and enter a valid workspace emoji and status of at most 100 characters.", http.StatusBadRequest)
		return
	}
	if errors.Is(err, service.ErrScheduledStatusLimit) {
		h.renderMembers(w, r, principal, nil, &submitted, "The status was not scheduled. Slack allows up to five scheduled statuses; edit or cancel one first.", http.StatusBadRequest)
		return
	}
	if err != nil {
		h.writeStoreError(w, err, "The status could not be scheduled right now.")
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

func (h Handler) updateScheduledStatus(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The scheduled status could not be read. Reload the page and try again.")
	if !ok {
		return
	}
	submitted := scheduledStatusSubmission(fields)
	startsAt, endsAt, err := scheduledStatusTimes(fields)
	if err == nil {
		_, err = h.Messages.UpdateScheduledUserStatus(r.Context(), principal.WorkspaceID, principal.UserID, domain.ScheduledStatusID(strings.TrimSpace(fields["id"])), fields["status_text"], fields["status_emoji"], startsAt, endsAt)
	}
	if errors.Is(err, service.ErrInvalidScheduledStatus) {
		h.renderMembers(w, r, principal, nil, &submitted, "The scheduled status was not updated. Choose a future start, a later end, and a valid workspace emoji.", http.StatusBadRequest)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		h.renderMembers(w, r, principal, nil, nil, "That scheduled status no longer exists. It may already have started or been cancelled.", http.StatusNotFound)
		return
	}
	if err != nil {
		h.writeStoreError(w, err, "The scheduled status could not be updated right now.")
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

func (h Handler) deleteScheduledStatus(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The scheduled status could not be read. Reload the page and try again.")
	if !ok {
		return
	}
	err = h.Messages.DeleteScheduledUserStatus(r.Context(), principal.WorkspaceID, principal.UserID, domain.ScheduledStatusID(strings.TrimSpace(fields["id"])))
	if errors.Is(err, store.ErrNotFound) {
		h.renderMembers(w, r, principal, nil, nil, "That scheduled status no longer exists. It may already have started or been cancelled.", http.StatusNotFound)
		return
	}
	if err != nil {
		h.writeStoreError(w, err, "The scheduled status could not be cancelled right now.")
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

func (h Handler) setPresence(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeUsersWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your availability could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	presence := domain.Presence(strings.TrimSpace(fields["presence"]))
	if presence != domain.PresenceAuto && presence != domain.PresenceAway {
		h.writePageError(w, http.StatusBadRequest, "That availability is not valid", "Choose Active or Away and try again.")
		return
	}
	if _, err := h.Messages.SetUserPresence(r.Context(), principal.WorkspaceID, principal.UserID, presence); err != nil {
		h.writeStoreError(w, err, "Your availability could not be updated.")
		return
	}
	http.Redirect(w, r, "/app/members", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Session
// ---------------------------------------------------------------------------

func (h Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	if _, err := h.Authenticator.Authenticate(r); err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The sign-out request could not be read. Reload the page and try again."); !ok {
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	redirectURL := "/signed-out"
	var logoutErr error
	if h.Login != nil {
		redirectURL, logoutErr = h.Login.logoutRedirectURL(r.Context(), sessionCookie.Value)
	}
	// The browser stops being signed in here, whatever the durable record does
	// next: leaving the cookie in place on a failed revocation strands the user
	// with a session they asked to end.
	cookie := auth.SessionCookie("", -1, h.CookieDomain)
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, cookie)
	w.Header().Set("Cache-Control", "no-store")
	if err := h.SessionRevoker.RevokeSession(r.Context(), sessionCookie.Value); err != nil && !errors.Is(err, store.ErrNotFound) {
		h.writePageError(w, http.StatusServiceUnavailable, "Sign-out is incomplete", "You are signed out of this browser, but the session record could not be revoked. Tell an administrator if this keeps happening.")
		return
	}
	if h.Login != nil && logoutErr != nil {
		redirectURL = "/signed-out?global=failed"
	}
	http.Redirect(w, r, redirectURL, http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

func (h Handler) validation(w http.ResponseWriter, r *http.Request) {
	h.identity(w, r, "SameOldChat is authenticated")
}

func (h Handler) me(w http.ResponseWriter, r *http.Request) {
	h.identity(w, r, "My profile")
}

// canShowIdentity is the one predicate behind both the identity page and the
// control that leads to it, so the workspace shell cannot advertise a page that
// answers "unavailable".
func (h Handler) canShowIdentity() bool {
	return h.Login != nil && h.Login.hasOpenIDConnectProvider() && immutableReleaseRevision.MatchString(h.ReleaseRevision)
}

func (h Handler) canShowWorkspaceAdmin(ctx context.Context, principal auth.Principal) bool {
	hasScope := false
	for _, scope := range []auth.Scope{auth.ScopeAdminAppsRead, auth.ScopeAdminAppsWrite, auth.ScopeAdminUsersRead, auth.ScopeAdminUsersWrite} {
		if principal.HasScope(scope) {
			hasScope = true
			break
		}
	}
	if !hasScope {
		return false
	}
	membership, err := h.Messages.WorkspaceMembership(ctx, principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil || !membership.Active {
		return false
	}
	return membership.Role == domain.WorkspaceRoleAdmin || membership.Role == domain.WorkspaceRoleOwner
}

func (h Handler) identity(w http.ResponseWriter, r *http.Request, heading string) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		if errors.Is(err, auth.ErrNotAuthenticated) && h.Login != nil {
			http.Redirect(w, r, "/signed-out", http.StatusSeeOther)
			return
		}
		h.writeAuthError(w, r, err)
		return
	}
	if !h.canShowIdentity() {
		h.writePageError(w, http.StatusServiceUnavailable, "Identity validation is unavailable", "This deployment does not have a Shauth provider and an immutable release revision, so a verified identity cannot be shown.")
		return
	}
	user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		h.writeStoreError(w, err, "Your identity is temporarily unavailable.")
		return
	}
	role, err := h.currentRole(r, principal)
	if err != nil {
		h.writeStoreError(w, err, "Your workspace role is temporarily unavailable.")
		return
	}
	sessionCookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
		h.writeAuthError(w, r, auth.ErrNotAuthenticated)
		return
	}
	avatarURL := profileImageURL(user.Profile)
	username := displayName(user)
	w.Header().Set("Cache-Control", "no-store")
	h.writeHTML(w, identityTemplate, identityData{Heading: heading, Username: username, Email: user.Email, Role: role, Release: h.ReleaseRevision, CSRFToken: auth.CSRFToken(sessionCookie.Value), AvatarURL: avatarURL, Avatar: initial(username)}, http.StatusOK, "identity rendering unavailable")
}

// currentRole reports the signed-in user's own workspace role in the vocabulary
// the identity page renders.
//
// It reads one membership as the user it is about, which is authority the user
// always holds over themselves. It used to page the whole workspace through
// AdminListUsers as the signed-in member, so /me and /auth/validation depended on
// an administrative read that a member must not be allowed to perform.
func (h Handler) currentRole(r *http.Request, principal auth.Principal) (string, error) {
	membership, err := h.Messages.WorkspaceMembership(r.Context(), principal.WorkspaceID, principal.UserID, principal.UserID)
	if err != nil {
		return "", err
	}
	switch membership.Role {
	case domain.WorkspaceRoleMember:
		return "developer", nil
	case domain.WorkspaceRoleAdmin, domain.WorkspaceRoleOwner:
		return "admin", nil
	default:
		return "", errors.New("identity has an unsupported workspace role")
	}
}

// ---------------------------------------------------------------------------
// Mutations
// ---------------------------------------------------------------------------

// joinConversation turns the read-only public-channel preview into a joined
// conversation. The page never submits a speculative message and then asks the
// user to repair membership after the fact: joining is an explicit, durable
// action, and a successful enhanced request reloads the membership-dependent
// controls from the server.
func (h Handler) joinConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The join request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	channel := h.requestChannel(r)
	if _, err := h.Messages.JoinConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That channel is not available", "Only a public channel you can see may be joined.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The channel could not be joined", "Your membership was not changed. Try again.")
		}
		return
	}
	target := h.viewURL(r, strings.TrimSpace(r.URL.Query().Get("thread")))
	h.redirectMutation(w, r, target)
}

// redirectMutation keeps navigation-producing mutations compatible with both
// ordinary forms and the enhanced workspace shell. A fetch follows a 303 on its
// own and hands the client a full HTML page with no destination; the explicit
// header lets the shell navigate only after the mutation has succeeded.
func (h Handler) redirectMutation(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h Handler) postMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your message could not be read from the form. Reload the page and send it again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	draftAttachments, attachmentErr := draftAttachmentsFromJSON(fields["draft_attachments"])
	if attachmentErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The staged files are not valid", "Reload the conversation and stage the files again.")
		return
	}
	command, commandText, isSlashCommand := slashCommandInput(fields["text"])
	var message domain.Message
	var slashRedirect string
	if len(draftAttachments) > 0 {
		switch {
		case !principal.HasScope(auth.ScopeFilesWrite):
			err = auth.ErrMissingScope
		case isSlashCommand:
			err = service.ErrInvalidExternalUpload
		default:
			completions := make([]domain.ExternalUploadCompletion, 0, len(draftAttachments))
			for _, attachment := range draftAttachments {
				completions = append(completions, domain.ExternalUploadCompletion{ID: attachment.UploadID, Title: attachment.Title})
			}
			_, err = h.Messages.CompleteExternalUploads(
				r.Context(), principal.WorkspaceID, principal.UserID, completions, []domain.ConversationID{channel},
				fields["text"], "", domain.MessageTimestamp(fields["thread_ts"]),
			)
		}
	} else if isSlashCommand {
		var handled bool
		message, slashRedirect, handled, err = h.dispatchBuiltInSlashCommand(r.Context(), principal, channel, domain.MessageTimestamp(fields["thread_ts"]), command, commandText, fields["timezone"])
		if !handled {
			err = h.Messages.DispatchSlashCommand(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(fields["thread_ts"]), command, commandText, h.responseBaseURL(r))
		}
	} else {
		message, err = h.Messages.Post(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["text"], domain.MessageTimestamp(fields["thread_ts"]), "")
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The message could not be sent because the workspace store is temporarily unavailable."
		if errors.Is(err, service.ErrInvalidMessage) {
			status = http.StatusBadRequest
			reason = "A message needs some text before it can be sent."
		}
		if errors.Is(err, service.ErrInvalidExternalUpload) {
			status = http.StatusBadRequest
			reason = "One or more staged files are no longer available. Remove them from the draft or stage them again."
		}
		if errors.Is(err, auth.ErrMissingScope) {
			status = http.StatusForbidden
			reason = "Your session cannot share files, so the staged attachments were not sent."
		}
		if errors.Is(err, service.ErrInvalidTimestamp) {
			status = http.StatusBadRequest
			reason = "That thread is not a message in this conversation."
		}
		if errors.Is(err, service.ErrInvalidSearch) {
			status = http.StatusBadRequest
			reason = "Add something to search for after /search."
		}
		if errors.Is(err, service.ErrInvalidLaterReminder) {
			status = http.StatusBadRequest
			reason = "Use /remind #channel what when, for example /remind #general stand-up tomorrow at 9am. Use /remind list to review channel reminders."
		}
		if errors.Is(err, service.ErrReminderTimeInPast) {
			status = http.StatusBadRequest
			reason = "Choose a reminder time in the future."
		}
		// Posting into a channel now requires membership of it, which is a
		// refusal the reader can act on and not an outage.
		if errors.Is(err, service.ErrNotInConversation) {
			status = http.StatusForbidden
			reason = "You are not a member of this conversation, so the message was not sent."
		}
		if errors.Is(err, service.ErrConversationAlreadyArchived) {
			status = http.StatusConflict
			reason = "This conversation is archived, so new messages cannot be sent."
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			reason = "That conversation is no longer available."
		}
		if errors.Is(err, service.ErrSlashCommandNotFound) {
			status = http.StatusNotFound
			reason = "That slash command is not installed in this workspace."
		}
		if errors.Is(err, service.ErrSlashCommandInThread) {
			status = http.StatusBadRequest
			reason = "Slash commands cannot be used in threads."
		}
		if errors.Is(err, service.ErrAppInteractionUnavailable) || errors.Is(err, service.ErrInvalidAppResponse) {
			status = http.StatusBadGateway
			reason = "The app did not accept that command. Your command was not posted as a message."
		}
		w.Header().Set("Vary", "HX-Request")
		if r.Header.Get("HX-Request") == "true" {
			// The composer renders this text next to the field and keeps the
			// draft, so a failure is never silent and never loses the message.
			secureHeaders(w, workspaceContentSecurityPolicy)
			http.Error(w, reason, status)
			return
		}
		// Re-rendering the workspace requires the scope that reads the
		// workspace. Without this the page — the whole conversation, the
		// sidebar and a live CSRF token — was the failure body for a principal
		// that GET /app answers with 403.
		reader, readerErr := requireHistoryReader(principal)
		if readerErr != nil {
			h.writePageError(w, status, "That message was not sent", reason)
			return
		}
		h.renderApp(w, r, reader, composerState{Draft: fields["text"], Attachments: draftAttachments, Message: reason, Status: status})
		return
	}
	if cleanupErr := h.Messages.DeleteDraft(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"]))); cleanupErr != nil {
		// The message already committed. Reporting the post as failed would ask
		// the browser to retry and risk a duplicate, so surface the independent
		// cleanup result without changing the mutation outcome.
		w.Header().Set("X-SameOldChat-Draft-Cleanup", "failed")
	}
	if len(draftAttachments) > 0 {
		h.redirectMutation(w, r, h.viewURL(r, strings.TrimSpace(fields["thread_ts"])))
		return
	}
	if isSlashCommand && message.ID == "" {
		if slashRedirect != "" {
			h.redirectMutation(w, r, slashRedirect)
			return
		}
		w.Header().Set("Vary", "HX-Request")
		if r.Header.Get("HX-Request") == "true" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, h.viewURL(r, ""), http.StatusSeeOther)
		return
	}
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		sessionCookie, cookieErr := r.Cookie(auth.SessionCookieName)
		if cookieErr != nil || strings.TrimSpace(sessionCookie.Value) == "" {
			h.writeAuthError(w, r, auth.ErrNotAuthenticated)
			return
		}
		conversation, conversationErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
		if conversationErr != nil {
			h.writeFragmentError(w, conversationErr, "the conversation is temporarily unavailable")
			return
		}
		thread := strings.TrimSpace(fields["thread_ts"])
		list, _ := h.newMessageList(r.Context(), principal, messageListRequest{Conversation: conversation, CSRFToken: auth.CSRFToken(sessionCookie.Value), Messages: []domain.Message{message}, Thread: thread, ThreadPane: thread != "", Member: true, Names: h.newUserNames(r.Context(), principal)})
		h.writeFragment(w, list)
		return
	}
	http.Redirect(w, r, h.viewURL(r, strings.TrimSpace(fields["thread_ts"])), http.StatusSeeOther)
}

func (h Handler) saveDraft(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your draft could not be read from the form. Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	thread := domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"]))
	if channel == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That draft has no destination", "Reload the conversation and try again.")
		return
	}
	attachments, attachmentErr := draftAttachmentsFromJSON(fields["draft_attachments"])
	if attachmentErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The draft attachments are not valid", "Reload the conversation and stage the files again.")
		return
	}
	if strings.TrimSpace(fields["text"]) == "" && len(attachments) == 0 {
		err = h.Messages.DeleteDraft(r.Context(), principal.WorkspaceID, principal.UserID, channel, thread)
	} else {
		_, err = h.Messages.SaveDraftWithAttachments(r.Context(), principal.WorkspaceID, principal.UserID, channel, thread, fields["text"], attachments)
	}
	if err != nil {
		status, reason := http.StatusServiceUnavailable, "The draft could not be saved because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrInvalidMessage), errors.Is(err, service.ErrInvalidTimestamp), errors.Is(err, service.ErrInvalidExternalUpload), errors.Is(err, store.ErrInvalidArgument):
			status, reason = http.StatusBadRequest, "The draft text or thread is not valid."
		case errors.Is(err, service.ErrNotInConversation), errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusForbidden, "You can no longer save a draft in this conversation."
		}
		h.writeMutationError(w, r, status, "The draft was not saved", reason)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) deleteDraft(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The draft could not be deleted from this form. Reload Drafts & sent and try again."); !ok {
		return
	}
	channel := h.requestChannel(r)
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread_ts")))
	if channel == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That draft link is not valid", "Open Drafts & sent from the workspace and try again.")
		return
	}
	if err := h.Messages.DeleteDraft(r.Context(), principal.WorkspaceID, principal.UserID, channel, thread); err != nil {
		h.writeMutationError(w, r, http.StatusServiceUnavailable, "The draft was not deleted", "The workspace store is temporarily unavailable.")
		return
	}
	returnChannel := strings.TrimSpace(r.URL.Query().Get("return_channel"))
	if returnChannel == "" {
		returnChannel = string(h.Channel)
	}
	query := url.Values{"channel": {returnChannel}, "tab": {"drafts"}, "draft_deleted": {"1"}}
	h.redirectMutation(w, r, "/app/drafts?"+query.Encode())
}

func (h Handler) scheduleMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Your scheduled message could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	attachments, attachmentErr := draftAttachmentsFromJSON(fields["draft_attachments"])
	if attachmentErr != nil {
		h.writeScheduleMessageError(w, r, principal, fields["text"], nil, fields["schedule_at"], http.StatusBadRequest, "The staged files are no longer valid. Reload the conversation and stage them again.")
		return
	}
	postAtUnix, parseErr := strconv.ParseInt(strings.TrimSpace(fields["post_at"]), 10, 64)
	if parseErr != nil || postAtUnix <= 0 {
		h.writeScheduleMessageError(w, r, principal, fields["text"], attachments, fields["schedule_at"], http.StatusBadRequest, "Choose a delivery date and time in your browser before scheduling the message.")
		return
	}
	channel := h.requestChannel(r)
	_, err = h.Messages.ScheduleMessageAs(r.Context(), principal.WorkspaceID, principal.UserID, domain.ScheduledMessageRequest{
		Channel:         channel,
		Text:            fields["text"],
		ThreadTimestamp: domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"])),
		PostAt:          time.Unix(postAtUnix, 0).UTC(),
		CredentialHash:  service.InternalScheduledCredential(principal.WorkspaceID, principal.UserID),
		FileAttachments: attachments,
	})
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The message could not be scheduled because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrInvalidMessage):
			status, reason = http.StatusBadRequest, "A scheduled message needs text or a staged file before it can be saved."
		case errors.Is(err, service.ErrInvalidExternalUpload):
			status, reason = http.StatusBadRequest, "One or more staged files are no longer available. Remove them or stage the files again."
		case errors.Is(err, service.ErrInvalidTimestamp):
			status, reason = http.StatusBadRequest, "That thread is not a message in this conversation."
		case errors.Is(err, service.ErrScheduledTimeInPast):
			status, reason = http.StatusBadRequest, "Choose a delivery time in the future."
		case errors.Is(err, service.ErrScheduledTimeTooFar):
			status, reason = http.StatusBadRequest, "Choose a delivery time within the next 120 days."
		case errors.Is(err, service.ErrScheduledTooMany):
			status, reason = http.StatusConflict, "This channel already has 30 messages scheduled in that five-minute window."
		case errors.Is(err, service.ErrNotInConversation):
			status, reason = http.StatusForbidden, "You are not a member of this conversation, so the message was not scheduled."
		case errors.Is(err, service.ErrConversationAlreadyArchived):
			status, reason = http.StatusConflict, "This conversation is archived, so messages cannot be scheduled in it."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That conversation or thread is no longer available."
		}
		h.writeScheduleMessageError(w, r, principal, fields["text"], attachments, fields["schedule_at"], status, reason)
		return
	}
	query := url.Values{"channel": {string(channel)}, "tab": {"scheduled"}, "scheduled": {"1"}}
	if err := h.Messages.DeleteDraft(r.Context(), principal.WorkspaceID, principal.UserID, channel, domain.MessageTimestamp(strings.TrimSpace(fields["thread_ts"]))); err != nil {
		query.Set("draft_cleanup", "failed")
	}
	h.redirectMutation(w, r, "/app/drafts?"+query.Encode())
}

func (h Handler) writeScheduleMessageError(w http.ResponseWriter, r *http.Request, principal auth.Principal, draft string, attachments []domain.DraftAttachment, scheduleAt string, status int, reason string) {
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, reason, status)
		return
	}
	reader, err := requireHistoryReader(principal)
	if err != nil {
		h.writePageError(w, status, "That message was not scheduled", reason)
		return
	}
	h.renderApp(w, r, reader, composerState{Draft: draft, Attachments: attachments, ScheduleAt: scheduleAt, Message: reason, Status: status})
}

func (h Handler) updateScheduledMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The scheduled message could not be read from this form. Reload Drafts & sent and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	id := domain.ScheduledMessageID(strings.TrimSpace(r.URL.Query().Get("id")))
	postAtUnix, parseErr := strconv.ParseInt(strings.TrimSpace(fields["post_at"]), 10, 64)
	if channel == "" || id == "" || parseErr != nil || postAtUnix <= 0 {
		h.writeMutationError(w, r, http.StatusBadRequest, "That scheduled message update is not valid", "Choose a message and a future delivery time.")
		return
	}
	_, err = h.Messages.UpdateScheduledMessage(r.Context(), principal.WorkspaceID, principal.UserID, id, channel, fields["text"], time.Unix(postAtUnix, 0).UTC())
	if err != nil {
		status, reason := http.StatusServiceUnavailable, "The scheduled message could not be updated because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrInvalidMessage), errors.Is(err, store.ErrInvalidArgument):
			status, reason = http.StatusBadRequest, "A scheduled message needs valid text."
		case errors.Is(err, service.ErrScheduledTimeInPast):
			status, reason = http.StatusBadRequest, "Choose a delivery time in the future."
		case errors.Is(err, service.ErrScheduledTimeTooFar):
			status, reason = http.StatusBadRequest, "Choose a delivery time within the next 120 days."
		case errors.Is(err, service.ErrScheduledTooMany):
			status, reason = http.StatusConflict, "This channel already has 30 messages scheduled in that five-minute window."
		case errors.Is(err, service.ErrNotInConversation):
			status, reason = http.StatusForbidden, "You are no longer a member of this conversation."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That scheduled message was already sent, cancelled, or changed in another client."
		}
		h.writeMutationError(w, r, status, "The scheduled message was not updated", reason)
		return
	}
	returnChannel := strings.TrimSpace(r.URL.Query().Get("return_channel"))
	if returnChannel == "" {
		returnChannel = string(h.Channel)
	}
	query := url.Values{"channel": {returnChannel}, "tab": {"scheduled"}, "updated": {"1"}}
	h.redirectMutation(w, r, "/app/drafts?"+query.Encode())
}

func (h Handler) sendScheduledMessageNow(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The scheduled message could not be sent from this form. Reload Drafts & sent and try again."); !ok {
		return
	}
	id := domain.ScheduledMessageID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That scheduled message link is not valid", "Open Drafts & sent from the workspace and try again.")
		return
	}
	if _, err := h.Messages.SendScheduledMessageNow(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		status, reason := http.StatusServiceUnavailable, "The scheduled message could not be sent because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrNotInConversation):
			status, reason = http.StatusForbidden, "You are no longer a member of this conversation."
		case errors.Is(err, service.ErrConversationAlreadyArchived):
			status, reason = http.StatusConflict, "This conversation is archived."
		case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrLeaseConflict):
			status, reason = http.StatusConflict, "That scheduled message was already sent, cancelled, or is being delivered."
		}
		h.writeMutationError(w, r, status, "The scheduled message was not sent", reason)
		return
	}
	returnChannel := strings.TrimSpace(r.URL.Query().Get("return_channel"))
	if returnChannel == "" {
		returnChannel = string(h.Channel)
	}
	query := url.Values{"channel": {returnChannel}, "tab": {"sent"}, "sent": {"1"}}
	h.redirectMutation(w, r, "/app/drafts?"+query.Encode())
}

func (h Handler) cancelScheduledMessage(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The scheduled message could not be cancelled from this form. Reload Scheduled and try again."); !ok {
		return
	}
	channel := h.requestChannel(r)
	id := domain.ScheduledMessageID(strings.TrimSpace(r.URL.Query().Get("id")))
	if channel == "" || id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That scheduled message link is not valid", "Open Drafts & sent from the workspace and try again.")
		return
	}
	if err := h.Messages.DeleteScheduledMessage(r.Context(), principal.WorkspaceID, principal.UserID, channel, id); err != nil {
		status, reason := http.StatusServiceUnavailable, "The scheduled message could not be cancelled because the workspace store is temporarily unavailable."
		if errors.Is(err, store.ErrNotFound) {
			status, reason = http.StatusNotFound, "That scheduled message was already sent, cancelled, or is no longer available."
		}
		if errors.Is(err, store.ErrInvalidArgument) {
			status, reason = http.StatusBadRequest, "That scheduled message link is not valid."
		}
		h.writeMutationError(w, r, status, "The scheduled message was not cancelled", reason)
		return
	}
	returnChannel := strings.TrimSpace(r.URL.Query().Get("return_channel"))
	if returnChannel == "" {
		returnChannel = string(h.Channel)
	}
	query := url.Values{"channel": {returnChannel}, "tab": {"scheduled"}, "cancelled": {"1"}}
	h.redirectMutation(w, r, "/app/drafts?"+query.Encode())
}

const (
	maxWorkspaceUploadBytes  = 100 << 20
	maxWorkspaceUploadFields = 1 << 20
	draftAttachmentTTL       = 15 * time.Minute
)

func (h Handler) stageDraftFiles(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if !principal.HasScope(auth.ScopeChatWrite) {
		h.writeAuthError(w, r, auth.ErrMissingScope)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUploadBytes+maxWorkspaceUploadFields)
	if err := r.ParseMultipartForm(maxWorkspaceUploadFields); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "Those files could not be read", "Choose up to ten files smaller than 100 MiB in total and try again.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !h.acceptCSRFResult(w, r, auth.ValidateCSRFValue(r, r.FormValue(auth.CSRFTokenFieldName))) {
		return
	}
	existing, err := draftAttachmentsFromJSON(r.FormValue("draft_attachments"))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The existing draft attachments are not valid", "Reload the conversation before adding files.")
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 || len(existing)+len(headers) > 10 {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose up to ten files", "A draft can contain between one and ten staged files.")
		return
	}
	attachments := append([]domain.DraftAttachment(nil), existing...)
	singleTitle := strings.TrimSpace(r.FormValue("title"))
	for index, header := range headers {
		name := strings.TrimSpace(header.Filename)
		if name == "" {
			name = "pasted-file-" + strconv.Itoa(index+1)
		}
		mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		upload, createErr := h.Messages.CreateExternalUpload(r.Context(), principal.WorkspaceID, principal.UserID, name, mimeType, header.Size, draftAttachmentTTL)
		if createErr != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That file was not staged", "Choose non-empty files with valid names and try again.")
			return
		}
		source, openErr := header.Open()
		if openErr != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That file could not be opened", "Choose the files again and retry.")
			return
		}
		uploadErr := h.Messages.UploadExternalFile(r.Context(), upload.ID, header.Size, source)
		closeErr := source.Close()
		if uploadErr == nil {
			uploadErr = closeErr
		}
		if uploadErr != nil {
			status, reason := http.StatusServiceUnavailable, "The file store is temporarily unavailable. Your existing draft is unchanged."
			if errors.Is(uploadErr, service.ErrInvalidExternalUpload) {
				status, reason = http.StatusBadRequest, "One selected file is empty or does not match its staged size."
			}
			h.writeMutationError(w, r, status, "Those files were not staged", reason)
			return
		}
		title := name
		if len(headers) == 1 && singleTitle != "" {
			title = singleTitle
		}
		attachments = append(attachments, domain.DraftAttachment{UploadID: upload.ID, Title: title})
	}
	channel := h.requestChannel(r)
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	draft, err := h.Messages.SaveDraftWithAttachments(r.Context(), principal.WorkspaceID, principal.UserID, channel, thread, r.FormValue("text"), attachments)
	if err != nil {
		status, reason := http.StatusServiceUnavailable, "The files were uploaded, but the draft store is temporarily unavailable. Try adding them again."
		if errors.Is(err, service.ErrNotInConversation) {
			status, reason = http.StatusForbidden, "You can no longer save a draft in this conversation."
		} else if errors.Is(err, service.ErrInvalidMessage) || errors.Is(err, service.ErrInvalidTimestamp) || errors.Is(err, service.ErrInvalidExternalUpload) || errors.Is(err, store.ErrInvalidArgument) {
			status, reason = http.StatusBadRequest, "The draft destination or staged files are no longer valid."
		}
		h.writeMutationError(w, r, status, "Those files were not added to the draft", reason)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			Attachments []draftAttachmentView `json:"attachments"`
		}{Attachments: newDraftAttachmentViews(draft.Attachments)})
		return
	}
	h.redirectMutation(w, r, h.viewURL(r, string(thread)))
}

func (h Handler) uploadFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if !principal.HasScope(auth.ScopeChatWrite) {
		h.writeAuthError(w, r, auth.ErrMissingScope)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUploadBytes+maxWorkspaceUploadFields)
	if err := r.ParseMultipartForm(maxWorkspaceUploadFields); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That file could not be read", "Choose one file smaller than 100 MiB and try again.")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if !h.acceptCSRFResult(w, r, auth.ValidateCSRFValue(r, r.FormValue(auth.CSRFTokenFieldName))) {
		return
	}
	headers := r.MultipartForm.File["file"]
	if len(headers) == 0 || len(headers) > 10 {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose up to ten files", "Between one and ten files can be staged in one message.")
		return
	}
	completions := make([]domain.ExternalUploadCompletion, 0, len(headers))
	singleTitle := strings.TrimSpace(r.FormValue("title"))
	for index, header := range headers {
		name := strings.TrimSpace(header.Filename)
		if name == "" {
			name = "pasted-file-" + strconv.Itoa(index+1)
		}
		mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		upload, err := h.Messages.CreateExternalUpload(r.Context(), principal.WorkspaceID, principal.UserID, name, mimeType, header.Size, 15*time.Minute)
		if err != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That file was not staged", "Choose non-empty files with valid names and try again.")
			return
		}
		source, err := header.Open()
		if err != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That file could not be opened", "Choose the files again and retry.")
			return
		}
		err = h.Messages.UploadExternalFile(r.Context(), upload.ID, header.Size, source)
		closeErr := source.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			status, reason := http.StatusServiceUnavailable, "The file store is temporarily unavailable. Nothing was sent."
			if errors.Is(err, service.ErrInvalidExternalUpload) {
				status, reason = http.StatusBadRequest, "One selected file is empty or does not match its staged size."
			}
			h.writeMutationError(w, r, status, "Those files were not uploaded", reason)
			return
		}
		title := name
		if len(headers) == 1 && singleTitle != "" {
			title = singleTitle
		}
		completions = append(completions, domain.ExternalUploadCompletion{ID: upload.ID, Title: title})
	}
	channel := h.requestChannel(r)
	thread := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("thread")))
	if _, err := h.Messages.CompleteExternalUploads(
		r.Context(), principal.WorkspaceID, principal.UserID, completions, []domain.ConversationID{channel},
		r.FormValue("initial_comment"), "", thread,
	); err != nil {
		reason := "The files remain staged but were not shared into the conversation."
		status := http.StatusServiceUnavailable
		if errors.Is(err, service.ErrNotInConversation) {
			status, reason = http.StatusForbidden, "You are not a member of this conversation, so the files were not sent."
		} else if errors.Is(err, service.ErrConversationAlreadyArchived) {
			status, reason = http.StatusConflict, "This conversation is archived, so the files were not sent."
		} else if errors.Is(err, service.ErrInvalidTimestamp) || errors.Is(err, service.ErrInvalidExternalUpload) {
			status, reason = http.StatusBadRequest, "That thread is not a message in this conversation."
		} else if errors.Is(err, store.ErrNotFound) {
			status, reason = http.StatusNotFound, "That conversation or staged file is no longer available."
		}
		h.writeMutationError(w, r, status, "Those files were not sent", reason)
		return
	}
	if cleanupErr := h.Messages.DeleteDraft(r.Context(), principal.WorkspaceID, principal.UserID, channel, thread); cleanupErr != nil && !errors.Is(cleanupErr, store.ErrNotFound) {
		// The file-share message is already committed. Report the recoverable
		// cleanup failure without pretending the upload failed or retrying it.
		w.Header().Set("X-SameOldChat-Draft-Cleanup", "failed")
	}
	h.redirectMutation(w, r, h.viewURL(r, string(thread)))
}

func (h Handler) downloadFile(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeFilesRead)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fileID := domain.FileID(strings.TrimSpace(r.PathValue("fileID")))
	file, source, err := h.Messages.OpenFile(r.Context(), principal.WorkspaceID, principal.UserID, fileID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			h.writePageError(w, http.StatusNotFound, "That file is not available", "It may have been deleted or is not shared with a conversation you can access.")
			return
		}
		h.writePageError(w, http.StatusServiceUnavailable, "That file could not be opened", "The file store is temporarily unavailable.")
		return
	}
	defer source.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": file.Name})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, source)
}

func slashCommandInput(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	end := strings.IndexAny(text, " \t\r\n")
	if end < 0 {
		return text, "", true
	}
	return text[:end], strings.TrimSpace(text[end:]), true
}

func builtInSlashCommands() []domain.AppShortcut {
	return []domain.AppShortcut{
		{AppName: "Slack", Name: "/mentions", Command: "/mentions", Description: "Open your mentions", Type: "slash"},
		{AppName: "Slack", Name: "/people", Command: "/people", Description: "Open the people directory", Type: "slash"},
		{AppName: "Slack", Name: "/remind", Command: "/remind", Description: "Set a channel reminder", UsageHint: "[#channel] [what] [when] or list", Type: "slash"},
		{AppName: "Slack", Name: "/search", Command: "/search", Description: "Search messages", UsageHint: "[search terms]", Type: "slash"},
		{AppName: "Slack", Name: "/shrug", Command: "/shrug", Description: "Add ¯\\_(ツ)_/¯ to your message", UsageHint: "[message]", Type: "slash"},
	}
}

// dispatchBuiltInSlashCommand keeps Slack-owned commands ahead of installed
// app commands. Only commands with a real first-party journey are listed and
// handled here; the rest remain explicit gaps instead of decorative menu
// entries that post a success-looking no-op.
func (h Handler) dispatchBuiltInSlashCommand(ctx context.Context, principal auth.Principal, channel domain.ConversationID, thread domain.MessageTimestamp, command, text, timeZone string) (domain.Message, string, bool, error) {
	switch strings.ToLower(command) {
	case "/shrug":
		body := strings.TrimSpace(text)
		if body != "" {
			body += " "
		}
		body += `¯\\\_(ツ)\_/¯`
		message, err := h.Messages.Post(ctx, principal.WorkspaceID, principal.UserID, channel, body, thread, "")
		return message, "", true, err
	case "/search":
		if strings.TrimSpace(text) == "" {
			return domain.Message{}, "", true, service.ErrInvalidSearch
		}
		values := url.Values{"q": {strings.TrimSpace(text)}, "channel": {string(channel)}}
		return domain.Message{}, "/app/search?" + values.Encode(), true, nil
	case "/people":
		return domain.Message{}, "/app/members", true, nil
	case "/mentions":
		return domain.Message{}, "/app/activity?channel=" + url.QueryEscape(string(channel)), true, nil
	case "/remind":
		if thread != "" {
			return domain.Message{}, "", true, service.ErrSlashCommandInThread
		}
		if strings.EqualFold(strings.TrimSpace(text), "list") {
			values := url.Values{"channel": {string(channel)}, "filter": {"channel-reminders"}}
			return domain.Message{}, "/app/later?" + values.Encode(), true, nil
		}
		request, parseErr := h.channelReminderRequest(ctx, principal, channel, text, timeZone, time.Now().UTC())
		if parseErr != nil {
			return domain.Message{}, "", true, parseErr
		}
		if _, createErr := h.Messages.CreateLaterReminder(ctx, principal.WorkspaceID, principal.UserID, request); createErr != nil {
			return domain.Message{}, "", true, createErr
		}
		values := url.Values{"channel": {string(channel)}, "filter": {"channel-reminders"}, "changed": {"reminder"}}
		return domain.Message{}, "/app/later?" + values.Encode(), true, nil
	default:
		return domain.Message{}, "", false, nil
	}
}

func (h Handler) channelReminderRequest(ctx context.Context, principal auth.Principal, currentChannel domain.ConversationID, input, timeZone string, now time.Time) (domain.LaterReminderRequest, error) {
	input = strings.TrimSpace(input)
	targetEnd := strings.IndexAny(input, " \t\r\n")
	if targetEnd <= 1 || input[0] != '#' {
		return domain.LaterReminderRequest{}, service.ErrInvalidLaterReminder
	}
	targetName := strings.TrimSpace(input[1:targetEnd])
	expression := strings.TrimSpace(input[targetEnd:])
	if targetName == "" || expression == "" {
		return domain.LaterReminderRequest{}, service.ErrInvalidLaterReminder
	}
	target, err := h.joinedChannelByName(ctx, principal, targetName)
	if err != nil {
		return domain.LaterReminderRequest{}, err
	}
	if target.ID == "" {
		return domain.LaterReminderRequest{}, store.ErrNotFound
	}
	if target.ID != currentChannel {
		member, memberErr := h.Messages.IsConversationMember(ctx, principal.WorkspaceID, principal.UserID, target.ID)
		if memberErr != nil {
			return domain.LaterReminderRequest{}, memberErr
		}
		if !member {
			return domain.LaterReminderRequest{}, service.ErrNotInConversation
		}
	}
	location, err := time.LoadLocation(strings.TrimSpace(timeZone))
	if err != nil {
		return domain.LaterReminderRequest{}, service.ErrInvalidLaterReminder
	}
	text, due, recurrence, err := parseChannelReminderExpression(expression, now, location)
	if err != nil {
		return domain.LaterReminderRequest{}, err
	}
	return domain.LaterReminderRequest{
		Target: domain.LaterReminderChannel, Channel: target.ID, Text: text,
		DueAt: due.UTC(), TimeZone: location.String(), Recurrence: recurrence,
	}, nil
}

func (h Handler) joinedChannelByName(ctx context.Context, principal auth.Principal, name string) (domain.Conversation, error) {
	request := domain.ConversationListRequest{
		Limit: 100, ExcludeArchived: true, MemberUserID: principal.UserID,
		Types: []domain.ConversationType{domain.ConversationTypePublic, domain.ConversationTypePrivate},
	}
	for {
		page, err := h.Messages.Conversations(ctx, principal.WorkspaceID, principal.UserID, request)
		if err != nil {
			return domain.Conversation{}, err
		}
		for _, conversation := range page.Conversations {
			if strings.EqualFold(conversation.Name, name) {
				return conversation, nil
			}
		}
		if !page.HasMore || page.NextCursor == "" || page.NextCursor == request.Cursor {
			return domain.Conversation{}, store.ErrNotFound
		}
		request.Cursor = page.NextCursor
	}
}

func parseChannelReminderExpression(expression string, now time.Time, location *time.Location) (string, time.Time, domain.ReminderRecurrence, error) {
	localNow := now.In(location)
	if match := remindInPattern.FindStringSubmatch(expression); match != nil {
		// "a" and "an" are the spoken form of one: "in an hour" means "in 1 hour".
		count := 1
		if quantity := strings.ToLower(match[2]); quantity != "a" && quantity != "an" {
			count, _ = strconv.Atoi(quantity)
		}
		unit := strings.ToLower(match[3])
		// A month and a year are calendar steps, not fixed spans: "in 2 months"
		// keeps the wall-clock time of day and lands on the same day-of-month two
		// months on, the way AddDate resolves a short month (Jan 31 + 1 month is
		// early March, matching how "every month" already steps here). The fixed
		// units stay an absolute duration, as they were.
		switch {
		case strings.HasPrefix(unit, "month"):
			return strings.TrimSpace(match[1]), localNow.AddDate(0, count, 0), domain.ReminderOnce, nil
		case strings.HasPrefix(unit, "year"):
			return strings.TrimSpace(match[1]), localNow.AddDate(count, 0, 0), domain.ReminderOnce, nil
		}
		duration := time.Duration(count) * time.Minute
		switch {
		case strings.HasPrefix(unit, "hour"):
			duration = time.Duration(count) * time.Hour
		case strings.HasPrefix(unit, "day"):
			duration = time.Duration(count) * 24 * time.Hour
		case strings.HasPrefix(unit, "week"):
			duration = time.Duration(count) * 7 * 24 * time.Hour
		}
		return strings.TrimSpace(match[1]), now.Add(duration), domain.ReminderOnce, nil
	}
	if match := remindTomorrowPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[2], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		tomorrow := localNow.AddDate(0, 0, 1)
		due, err := reminderLocalTime(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), hour, minute, location)
		return strings.TrimSpace(match[1]), due, domain.ReminderOnce, err
	}
	if match := remindDatePattern.FindStringSubmatch(expression); match != nil {
		date, err := time.ParseInLocation("2006-01-02", match[2], location)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
		return strings.TrimSpace(match[1]), due, domain.ReminderOnce, err
	}
	if match := remindWeekdayPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		due, err := comingWeekday(match[2], hour, minute, localNow, now, location)
		if err != nil {
			return "", time.Time{}, "", err
		}
		return strings.TrimSpace(match[1]), due, domain.ReminderWeekly, nil
	}
	if match := remindRecurringPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		recurrence := map[string]domain.ReminderRecurrence{
			"day": domain.ReminderDaily, "week": domain.ReminderWeekly,
			"month": domain.ReminderMonthly, "year": domain.ReminderYearly,
		}[strings.ToLower(match[2])]
		due, err := reminderLocalTime(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, location)
		if err != nil {
			return "", time.Time{}, "", err
		}
		for !due.After(now) {
			switch recurrence {
			case domain.ReminderDaily:
				due = due.AddDate(0, 0, 1)
			case domain.ReminderWeekly:
				due = due.AddDate(0, 0, 7)
			case domain.ReminderMonthly:
				due = due.AddDate(0, 1, 0)
			case domain.ReminderYearly:
				due = due.AddDate(1, 0, 0)
			}
		}
		return strings.TrimSpace(match[1]), due, recurrence, nil
	}
	if match := remindOnWeekdayPattern.FindStringSubmatch(expression); match != nil {
		// "on friday" is a single occurrence on the coming Friday, distinct from
		// "every friday". It shares the coming-weekday resolution with the
		// recurring form but records no recurrence.
		hour, minute, err := parseReminderClock(match[3], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		due, err := comingWeekday(match[2], hour, minute, localNow, now, location)
		if err != nil {
			return "", time.Time{}, "", err
		}
		return strings.TrimSpace(match[1]), due, domain.ReminderOnce, nil
	}
	if match := remindOnMonthDayPattern.FindStringSubmatch(expression); match != nil {
		// "on July 4" is a single occurrence on the next such date: this year if it
		// is still ahead, otherwise the next. An impossible day for the month —
		// "on February 30" — is rejected rather than rolled into March.
		month := monthByName(match[2])
		day, _ := strconv.Atoi(match[3])
		hour, minute, err := parseReminderClock(match[4], 9, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(localNow.Year(), month, day, hour, minute, location)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		if !due.After(now) {
			due, err = reminderLocalTime(localNow.Year()+1, month, day, hour, minute, location)
			if err != nil {
				return "", time.Time{}, "", service.ErrInvalidLaterReminder
			}
		}
		return strings.TrimSpace(match[1]), due, domain.ReminderOnce, nil
	}
	if match := remindTodayPattern.FindStringSubmatch(expression); match != nil {
		hour, minute, err := parseReminderClock(match[2], 0, 0)
		if err != nil {
			return "", time.Time{}, "", service.ErrInvalidLaterReminder
		}
		due, err := reminderLocalTime(localNow.Year(), localNow.Month(), localNow.Day(), hour, minute, location)
		if err != nil || !due.After(now) {
			return "", time.Time{}, "", service.ErrReminderTimeInPast
		}
		return strings.TrimSpace(match[1]), due, domain.ReminderOnce, nil
	}
	return "", time.Time{}, "", service.ErrInvalidLaterReminder
}

// comingWeekday resolves the next occurrence of a named weekday at the given
// clock time, rolling to the following week when this week's time has already
// passed. Both "every <weekday>" and "on <weekday>" position their first
// delivery this way.
func comingWeekday(name string, hour, minute int, localNow, now time.Time, location *time.Location) (time.Time, error) {
	weekday := map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday,
		"wednesday": time.Wednesday, "thursday": time.Thursday,
		"friday": time.Friday, "saturday": time.Saturday,
	}[strings.ToLower(name)]
	days := (int(weekday) - int(localNow.Weekday()) + 7) % 7
	date := localNow.AddDate(0, 0, days)
	due, err := reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
	if err != nil {
		return time.Time{}, err
	}
	if !due.After(now) {
		date = date.AddDate(0, 0, 7)
		due, err = reminderLocalTime(date.Year(), date.Month(), date.Day(), hour, minute, location)
	}
	return due, err
}

// monthByName maps a full or abbreviated month name to its time.Month. The
// pattern only hands it a name it already matched, so the zero return is
// unreachable and exists to keep the switch total.
func monthByName(name string) time.Month {
	switch strings.ToLower(name) {
	case "january", "jan":
		return time.January
	case "february", "feb":
		return time.February
	case "march", "mar":
		return time.March
	case "april", "apr":
		return time.April
	case "may":
		return time.May
	case "june", "jun":
		return time.June
	case "july", "jul":
		return time.July
	case "august", "aug":
		return time.August
	case "september", "sep", "sept":
		return time.September
	case "october", "oct":
		return time.October
	case "november", "nov":
		return time.November
	case "december", "dec":
		return time.December
	}
	return time.Month(0)
}

func parseReminderClock(value string, defaultHour, defaultMinute int) (int, int, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultHour, defaultMinute, nil
	}
	for _, layout := range []string{"15:04", "3pm", "3:04pm"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Hour(), parsed.Minute(), nil
		}
	}
	return 0, 0, service.ErrInvalidLaterReminder
}

func reminderLocalTime(year int, month time.Month, day, hour, minute int, location *time.Location) (time.Time, error) {
	value := time.Date(year, month, day, hour, minute, 0, 0, location)
	local := value.In(location)
	if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
		return time.Time{}, service.ErrInvalidLaterReminder
	}
	return value, nil
}

func (h Handler) appInteraction(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeAppInteractionMutation(w, r)
	if !ok {
		return
	}
	value := fields["value"]
	if strings.TrimSpace(fields["action_type"]) == "datetimepicker" && strings.TrimSpace(value) != "" {
		if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err != nil {
			parsed, parseErr := time.ParseInLocation("2006-01-02T15:04", strings.TrimSpace(value), time.Local)
			if parseErr != nil {
				h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Choose a valid date and time.")
				return
			}
			value = strconv.FormatInt(parsed.Unix(), 10)
		}
	}
	action := domain.AppBlockAction{
		MessageID: domain.MessageID(strings.TrimSpace(fields["message_id"])),
		BlockID:   strings.TrimSpace(fields["block_id"]),
		ActionID:  strings.TrimSpace(fields["action_id"]),
		Type:      strings.TrimSpace(fields["action_type"]),
		Value:     value,
	}
	if err := h.Messages.DispatchBlockAction(r.Context(), principal.WorkspaceID, principal.UserID, action, h.responseBaseURL(r)); err != nil {
		status := http.StatusBadGateway
		reason := "The app did not accept that action. Nothing was changed."
		switch {
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That app message is no longer available."
		case errors.Is(err, service.ErrAppInteractionUnavailable):
			status, reason = http.StatusConflict, "This app has no interactive endpoint available."
		case errors.Is(err, service.ErrInvalidAppResponse):
			status, reason = http.StatusBadGateway, "The app returned a response that could not be applied."
		}
		h.writeMutationError(w, r, status, "That app action did not run", reason)
		return
	}
	target := h.viewURL(r, "")
	h.redirectMutation(w, r, target)
}

func (h Handler) appShortcut(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "That shortcut could not be read from the form. Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(fields["channel"]))
	appID := domain.AppID(strings.TrimSpace(fields["app_id"]))
	callbackID := strings.TrimSpace(fields["callback_id"])
	if channel == "" || appID == "" || callbackID == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That shortcut is incomplete", "Reload the conversation and try again.")
		return
	}
	if err := h.Messages.DispatchAppShortcut(
		r.Context(), principal.WorkspaceID, principal.UserID, channel, appID, callbackID,
		domain.MessageID(strings.TrimSpace(fields["message_id"])), h.responseBaseURL(r),
	); err != nil {
		status := http.StatusBadGateway
		reason := "The app did not accept that shortcut. Nothing was changed."
		switch {
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That app shortcut is no longer available."
		case errors.Is(err, service.ErrAppInteractionUnavailable):
			status, reason = http.StatusConflict, "This app has no interactive endpoint available."
		case errors.Is(err, store.ErrConflict):
			status, reason = http.StatusConflict, "That shortcut configuration is ambiguous."
		}
		h.writeMutationError(w, r, status, "That app shortcut did not run", reason)
		return
	}
	h.redirectMutation(w, r, h.viewURL(r, ""))
}

func (h Handler) decodeAppInteractionMutation(w http.ResponseWriter, r *http.Request) (map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(maxFormBody)
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the conversation and try again.")
		return nil, false
	}
	fields := make(map[string]string, len(r.Form))
	for name, values := range r.Form {
		if name == "value" {
			switch len(values) {
			case 0:
			case 1:
				fields[name] = values[0]
			default:
				encoded, err := json.Marshal(values)
				if err != nil {
					h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the conversation and try again.")
					return nil, false
				}
				fields[name] = string(encoded)
			}
			continue
		}
		if len(values) != 1 {
			h.writeMutationError(w, r, http.StatusBadRequest, "That app action could not be read", "Reload the conversation and try again.")
			return nil, false
		}
		fields[name] = values[0]
	}
	if !h.requireCSRF(w, r) {
		return nil, false
	}
	return fields, true
}

func (h Handler) appResponse(w http.ResponseWriter, r *http.Request) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	body, err := io.ReadAll(io.LimitReader(r.Body, service.MaxMessageBodyBytes+1))
	if err != nil || len(body) > service.MaxMessageBodyBytes {
		http.Error(w, "invalid response payload", http.StatusBadRequest)
		return
	}
	if err := h.Messages.HandleAppResponse(r.Context(), r.PathValue("token"), string(body)); err != nil {
		if errors.Is(err, service.ErrInvalidAppResponse) {
			http.Error(w, "response URL is invalid, expired, or exhausted", http.StatusNotFound)
			return
		}
		http.Error(w, "response could not be applied", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h Handler) responseBaseURL(r *http.Request) string {
	if h.PublicURL != "" {
		return h.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

func (h Handler) updateMessage(w http.ResponseWriter, r *http.Request) {
	principal, fields, channel, timestamp, ok := h.messageMutation(w, r, "The edited message could not be read from the form.")
	if !ok {
		return
	}
	if _, err := h.Messages.Update(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp, fields["text"]); err != nil {
		h.writeMessageMutationError(w, r, err, "edited")
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) deleteMessage(w http.ResponseWriter, r *http.Request) {
	principal, _, channel, timestamp, ok := h.messageMutation(w, r, "The delete request could not be read from the form.")
	if !ok {
		return
	}
	if _, err := h.Messages.Delete(r.Context(), principal.WorkspaceID, principal.UserID, channel, timestamp); err != nil {
		h.writeMessageMutationError(w, r, err, "deleted")
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) messageMutation(w http.ResponseWriter, r *http.Request, invalid string) (auth.Principal, map[string]string, domain.ConversationID, domain.MessageTimestamp, bool) {
	principal, err := h.authenticate(r, auth.ScopeChatWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return auth.Principal{}, nil, "", "", false
	}
	fields, ok := h.decodeMutation(w, r, invalid+" Reload the page and try again.")
	if !ok {
		return auth.Principal{}, nil, "", "", false
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The message was not changed because the link does not identify a message in this conversation.")
		return auth.Principal{}, nil, "", "", false
	}
	return principal, fields, h.requestChannel(r), timestamp, true
}

func (h Handler) writeMessageMutationError(w http.ResponseWriter, r *http.Request, err error, action string) {
	status := http.StatusServiceUnavailable
	heading := "The message was not " + action
	reason := "The message could not be " + action + " because the workspace store is temporarily unavailable."
	switch {
	case errors.Is(err, service.ErrInvalidMessage):
		status = http.StatusBadRequest
		reason = "A message needs some text before it can be saved."
	case errors.Is(err, service.ErrInvalidTimestamp):
		status = http.StatusBadRequest
		reason = "That message link is not valid."
	case errors.Is(err, service.ErrMessageNotOwned):
		status = http.StatusForbidden
		reason = "Only the person who posted this message can change it."
	case errors.Is(err, service.ErrNotInConversation):
		status = http.StatusForbidden
		reason = "You are no longer a member of this conversation."
	case errors.Is(err, store.ErrNotFound), errors.Is(err, service.ErrMessageAlreadyDeleted):
		status = http.StatusNotFound
		heading = "That message is no longer available"
		reason = "The message may already have been deleted."
	}
	h.writeMutationError(w, r, status, heading, reason)
}

func (h Handler) addReaction(w http.ResponseWriter, r *http.Request) {
	h.mutateReaction(w, r, true)
}

func (h Handler) removeReaction(w http.ResponseWriter, r *http.Request) {
	h.mutateReaction(w, r, false)
}

func (h Handler) mutateReaction(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopeReactionsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The reaction could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	name := strings.TrimSpace(fields["name"])
	if name == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reaction has no name", "A reaction needs a name, such as :wave:. Nothing was changed.")
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The reaction could not be applied because the link does not identify a message in this conversation.")
		return
	}
	if add {
		err = h.Messages.AddReaction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp, name)
	} else {
		err = h.Messages.RemoveReaction(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp, name)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The reaction could not be saved because the workspace store is temporarily unavailable."
		heading := "The reaction was not saved"
		if errors.Is(err, service.ErrInvalidReaction) {
			status = http.StatusBadRequest
			reason = "That reaction name is not valid."
		}
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That message is no longer available"
			reason = "That message or reaction is no longer available."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) addPin(w http.ResponseWriter, r *http.Request) {
	h.mutatePin(w, r, true)
}

func (h Handler) removePin(w http.ResponseWriter, r *http.Request) {
	h.mutatePin(w, r, false)
}

func (h Handler) mutatePin(w http.ResponseWriter, r *http.Request, add bool) {
	principal, err := h.authenticate(r, auth.ScopePinsWrite)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The pin could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The pin could not be applied because the link does not identify a message in this conversation.")
		return
	}
	if add {
		err = h.Messages.AddPin(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp)
	} else {
		err = h.Messages.RemovePin(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp)
	}
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The pin could not be saved because the workspace store is temporarily unavailable."
		heading := "The pin was not saved"
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That message is no longer available"
			reason = "That message or pin is no longer available."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) createLaterReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The reminder could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	request, err := personalReminderRequest(fields, time.Now().UTC())
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder time is not valid", err.Error())
		return
	}
	if sourceTimestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts"))); sourceTimestamp != "" {
		if _, parseErr := domain.ParseMessageTimestamp(sourceTimestamp); parseErr != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "Open the message menu again and choose a reminder time.")
			return
		}
		request.SourceChannel = h.requestChannel(r)
		request.SourceTimestamp = sourceTimestamp
		if request.Text == "" {
			request.Text = "Message reminder"
		}
	}
	if _, err := h.Messages.CreateLaterReminder(r.Context(), principal.WorkspaceID, principal.UserID, request); err != nil {
		h.writeLaterReminderError(w, r, err, "The reminder was not created")
		return
	}
	h.redirectReminderMutation(w, r, "reminder")
}

func (h Handler) updateLaterReminder(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The reminder changes could not be read from the form. Reload Later and try again.")
	if !ok {
		return
	}
	request, err := personalReminderRequest(fields, time.Now().UTC())
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder time is not valid", err.Error())
		return
	}
	id := domain.LaterReminderID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder link is not valid", "Open Later and edit the reminder again.")
		return
	}
	if _, err := h.Messages.UpdateLaterReminder(r.Context(), principal.WorkspaceID, principal.UserID, id, request); err != nil {
		h.writeLaterReminderError(w, r, err, "The reminder was not updated")
		return
	}
	h.redirectReminderMutation(w, r, "reminder")
}

func (h Handler) completeLaterReminder(w http.ResponseWriter, r *http.Request) {
	h.mutateLaterReminder(w, r, true)
}

func (h Handler) deleteLaterReminder(w http.ResponseWriter, r *http.Request) {
	h.mutateLaterReminder(w, r, false)
}

func (h Handler) mutateLaterReminder(w http.ResponseWriter, r *http.Request, complete bool) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The reminder action could not be read from the form. Reload Later and try again."); !ok {
		return
	}
	id := domain.LaterReminderID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That reminder link is not valid", "Open Later and try again.")
		return
	}
	if complete {
		err = h.Messages.CompleteLaterReminder(r.Context(), principal.WorkspaceID, principal.UserID, id)
	} else {
		err = h.Messages.DeleteLaterReminder(r.Context(), principal.WorkspaceID, principal.UserID, id)
	}
	if err != nil {
		heading := "The reminder was not deleted"
		if complete {
			heading = "The reminder was not completed"
		}
		h.writeLaterReminderError(w, r, err, heading)
		return
	}
	changed := "reminder-deleted"
	if complete {
		changed = "reminder-completed"
	}
	h.redirectReminderMutation(w, r, changed)
}

func personalReminderRequest(fields map[string]string, now time.Time) (domain.LaterReminderRequest, error) {
	timeZone := strings.TrimSpace(fields["timezone"])
	if timeZone == "" {
		timeZone = "UTC"
	}
	location, err := time.LoadLocation(timeZone)
	if err != nil {
		return domain.LaterReminderRequest{}, errors.New("Choose a valid time zone and try again.")
	}
	preset := strings.TrimSpace(fields["preset"])
	var due time.Time
	switch preset {
	case "20m":
		due = now.Add(20 * time.Minute)
	case "1h":
		due = now.Add(time.Hour)
	case "tomorrow":
		local := now.In(location).AddDate(0, 0, 1)
		due = time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, location)
	case "", "custom":
		date := strings.TrimSpace(fields["date"])
		clock := strings.TrimSpace(fields["time"])
		if clock == "" {
			clock = "09:00"
		}
		due, err = time.ParseInLocation("2006-01-02 15:04", date+" "+clock, location)
		if err != nil || due.In(location).Format("2006-01-02 15:04") != date+" "+clock {
			return domain.LaterReminderRequest{}, errors.New("Choose a real calendar date and time.")
		}
	default:
		return domain.LaterReminderRequest{}, errors.New("Choose one of the available reminder times.")
	}
	return domain.LaterReminderRequest{
		Target: domain.LaterReminderPersonal, Text: strings.TrimSpace(fields["text"]),
		DueAt: due.UTC(), TimeZone: timeZone,
		Recurrence: domain.ReminderRecurrence(strings.TrimSpace(fields["recurrence"])),
	}, nil
}

func (h Handler) writeLaterReminderError(w http.ResponseWriter, r *http.Request, err error, heading string) {
	status := http.StatusServiceUnavailable
	reason := "The reminder could not be changed because the workspace store is temporarily unavailable."
	switch {
	case errors.Is(err, service.ErrInvalidLaterReminder):
		status, reason = http.StatusBadRequest, "Add a description, a valid date and time, and a supported repeat option."
	case errors.Is(err, service.ErrReminderTimeInPast):
		status, reason = http.StatusBadRequest, "Choose a reminder time in the future."
	case errors.Is(err, service.ErrNotInConversation):
		status, reason = http.StatusForbidden, "You cannot create a reminder for a conversation you have not joined."
	case errors.Is(err, store.ErrNotFound):
		status, reason = http.StatusNotFound, "That reminder or source message is no longer available, belongs to another member, or is being delivered now."
	}
	h.writeMutationError(w, r, status, heading, reason)
}

func (h Handler) redirectReminderMutation(w http.ResponseWriter, r *http.Request, changed string) {
	state, ok := parseLaterState(r.URL.Query().Get("return_state"))
	if !ok {
		state = domain.SavedItemInProgress
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel == "" {
		channel = string(h.Channel)
	}
	query := url.Values{"channel": {channel}, "state": {string(state)}, "changed": {changed}}
	h.redirectMutation(w, r, "/app/later?"+query.Encode())
}

func (h Handler) saveForLater(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The save request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	timestamp := domain.MessageTimestamp(strings.TrimSpace(r.URL.Query().Get("ts")))
	if _, err := domain.ParseMessageTimestamp(timestamp); err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That message link is not valid", "The message was not saved because the link does not identify a message in this conversation.")
		return
	}
	if _, err := h.Messages.SaveForLater(r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), timestamp); err != nil {
		status, reason := http.StatusServiceUnavailable, "The message could not be saved because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrInvalidTimestamp):
			status, reason = http.StatusBadRequest, "That message link is not valid."
		case errors.Is(err, store.ErrNotFound):
			status, reason = http.StatusNotFound, "That message is no longer available or you can no longer read it."
		}
		h.writeMutationError(w, r, status, "The message was not saved", reason)
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) setSavedItemState(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The Later action could not be read from the form. Reload Later and try again."); !ok {
		return
	}
	id := domain.SavedItemID(strings.TrimSpace(r.URL.Query().Get("id")))
	rawState := strings.TrimSpace(r.URL.Query().Get("state"))
	state, ok := parseLaterState(rawState)
	if id == "" || rawState == "" || !ok {
		h.writeMutationError(w, r, http.StatusBadRequest, "That Later action is not valid", "Open Later from the workspace and try again.")
		return
	}
	if _, err := h.Messages.SetSavedItemState(r.Context(), principal.WorkspaceID, principal.UserID, id, state); err != nil {
		status, reason := http.StatusServiceUnavailable, "The saved item could not be moved because the workspace store is temporarily unavailable."
		if errors.Is(err, store.ErrNotFound) {
			status, reason = http.StatusNotFound, "That saved item is no longer available."
		} else if errors.Is(err, store.ErrInvalidArgument) {
			status, reason = http.StatusBadRequest, "That Later destination is not valid."
		}
		h.writeMutationError(w, r, status, "The saved item was not moved", reason)
		return
	}
	h.redirectLaterMutation(w, r, "state")
}

func (h Handler) removeSavedItem(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The remove request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	id := domain.SavedItemID(strings.TrimSpace(r.URL.Query().Get("id")))
	if id == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "That saved item link is not valid", "Open Later from the workspace and try again.")
		return
	}
	if err := h.Messages.RemoveSavedItem(r.Context(), principal.WorkspaceID, principal.UserID, id); err != nil {
		status, reason := http.StatusServiceUnavailable, "The saved item could not be removed because the workspace store is temporarily unavailable."
		if errors.Is(err, store.ErrNotFound) {
			status, reason = http.StatusNotFound, "That saved item was already removed or belongs to another member."
		}
		h.writeMutationError(w, r, status, "The saved item was not removed", reason)
		return
	}
	if strings.TrimSpace(r.URL.Query().Get("return_state")) != "" {
		h.redirectLaterMutation(w, r, "removed")
		return
	}
	h.completeMutation(w, r)
}

func (h Handler) redirectLaterMutation(w http.ResponseWriter, r *http.Request, changed string) {
	state, ok := parseLaterState(r.URL.Query().Get("return_state"))
	if !ok {
		state = domain.SavedItemInProgress
	}
	query := url.Values{
		"channel": {string(h.requestChannel(r))},
		"state":   {string(state)},
		"changed": {changed},
	}
	h.redirectMutation(w, r, "/app/later?"+query.Encode())
}

func (h Handler) openConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The conversation could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	rawUsers := strings.TrimSpace(fields["users"])
	if rawUsers == "" {
		selected := make([]string, 0)
		for field, value := range fields {
			if strings.HasPrefix(field, "user_") && value == "1" {
				selected = append(selected, strings.TrimPrefix(field, "user_"))
			}
		}
		sort.Strings(selected)
		rawUsers = strings.Join(selected, ",")
	}
	users, err := normalizeUserIDs(rawUsers)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That conversation has no members", "A direct conversation needs at least one member. Nothing was changed.")
		return
	}
	conversation, err := h.Messages.OpenConversation(r.Context(), principal.WorkspaceID, principal.UserID, users)
	if err != nil {
		status := http.StatusServiceUnavailable
		reason := "The conversation could not be opened because the workspace store is temporarily unavailable."
		if errors.Is(err, service.ErrInvalidConversation) {
			status = http.StatusBadRequest
			reason = "That set of members cannot be opened as a conversation."
		}
		heading := "The conversation was not opened"
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
			heading = "That member is no longer here"
			reason = "One of those members is no longer in the workspace."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	if name := strings.TrimSpace(fields["name"]); name != "" && conversation.Kind == domain.ConversationTypeMPIM {
		conversation, err = h.Messages.RenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, conversation.ID, name)
		if err != nil {
			h.writeMutationError(w, r, http.StatusBadRequest, "The group DM was opened but could not be named", "Use a name between one and 80 characters. You can name it from Direct messages.")
			return
		}
	}
	h.redirectMutation(w, r, appURL(string(conversation.ID), "", "", "", ""))
}

func (h Handler) addPeopleToDirectConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The people and history choice could not be read. Open the DM details and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	selected := make([]domain.UserID, 0)
	for field, value := range fields {
		if strings.HasPrefix(field, "user_") && value == "1" {
			selected = append(selected, domain.UserID(strings.TrimPrefix(field, "user_")))
		}
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left] < selected[right] })
	history := domain.DirectHistorySelection(strings.TrimSpace(fields["history"]))
	if len(selected) == 0 {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose who to add", "No new group DM was created. Return to the conversation details and choose at least one person.")
		return
	}
	if fields["confirm"] != "true" {
		conversation, err := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
		if err != nil || (!conversation.IsDirectOrGroup()) {
			h.writeMutationError(w, r, http.StatusNotFound, "That direct message is no longer available", "Nothing was changed.")
			return
		}
		additions := make([]memberView, 0, len(selected))
		for _, id := range selected {
			user, err := h.Messages.UserInfo(r.Context(), principal.WorkspaceID, principal.UserID, id)
			if err != nil || user.Deleted {
				h.writeMutationError(w, r, http.StatusNotFound, "One of those people is no longer available", "No new group DM was created. Return to the conversation and choose active workspace members.")
				return
			}
			additions = append(additions, memberView{ID: string(id), Name: displayName(user)})
		}
		sessionCookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil || strings.TrimSpace(sessionCookie.Value) == "" {
			h.writeAuthError(w, r, auth.ErrNotAuthenticated)
			return
		}
		sourceName := conversationName(conversation)
		if conversation.Name == "" || conversation.Name == "direct" {
			sourceName = h.participantNames(r.Context(), principal, channel)
		}
		chooseHistory := fields["stage"] != "review"
		if !chooseHistory && !history.Valid() {
			h.writeMutationError(w, r, http.StatusBadRequest, "Choose what conversation history to include", "No new group DM was created. Return to the conversation details and try again.")
			return
		}
		h.writeHTML(w, directExpansionReviewTemplate, directExpansionReviewData{
			SourceID: string(channel), SourceName: sourceName, Additions: additions,
			History: string(history), IncludeHistory: history == domain.DirectHistoryAll,
			ChooseHistory: chooseHistory,
			CSRFToken:     auth.CSRFToken(sessionCookie.Value),
		}, http.StatusOK, "the new group DM review is temporarily unavailable")
		return
	}
	if !history.Valid() {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose what conversation history to include", "No new group DM was created. Return to the conversation details and try again.")
		return
	}
	conversation, err := h.Messages.AddPeopleToDirectConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, selected, history)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this direct message", "No new group DM was created.")
		case errors.Is(err, service.ErrInvalidConversation):
			h.writeMutationError(w, r, http.StatusBadRequest, "Those people cannot be added", "A group DM can contain no more than nine people, and at least one selected person must be new.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That direct message or member is no longer available", "No history, membership, or files were changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The new group DM was not created", "The workspace store is temporarily unavailable. No partial conversation was created.")
		}
		return
	}
	h.redirectMutation(w, r, appURL(string(conversation.ID), "", "", "", ""))
}

// setConversationVisibility changes a channel between public and private. Both
// directions are one control because they are one decision — who may read this
// — and separating them would hide the fact that one of them is reversible and
// the other is not.
func (h Handler) setConversationVisibility(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := domain.ConversationID(strings.TrimSpace(r.URL.Query().Get("channel")))
	if strings.TrimSpace(fields["private"]) == "true" {
		_, err = h.Messages.AdminConvertConversationToPrivate(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	} else {
		_, err = h.Messages.AdminConvertConversationToPublic(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	}
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "The channel was not changed", "Only a workspace administrator can change who may read a channel, and a channel shared with another organization cannot be made public.")
		return
	}
	h.redirectMutation(w, r, appURL(string(channel), "", "", "", "")+"&details=1")
}

func (h Handler) convertGroupDirectToPrivate(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The private channel name could not be read. Open the group DM settings and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	conversation, err := h.Messages.ConvertGroupDirectToPrivate(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["name"])
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this group DM", "Nothing was converted.")
		case errors.Is(err, service.ErrNotWorkspaceAdmin):
			h.writeMutationError(w, r, http.StatusForbidden, "Your guest role cannot create this private channel", "Ask a full member or multi-channel guest in the group DM to convert it.")
		case errors.Is(err, service.ErrInvalidConversation), errors.Is(err, store.ErrInvalidConversationType):
			h.writeMutationError(w, r, http.StatusBadRequest, "That group DM cannot be converted", "Only a group direct message can become a private channel, and it needs a valid channel name.")
		case errors.Is(err, store.ErrAlreadyExists):
			h.writeMutationError(w, r, http.StatusConflict, "That channel name is already in use", "Choose another private channel name. The group DM was not changed.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That group DM is no longer available", "Nothing was converted.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The group DM was not converted", "The workspace store is temporarily unavailable. Messages, files, membership, and the conversation type remain unchanged.")
		}
		return
	}
	h.redirectMutation(w, r, appURL(string(conversation.ID), "", "", "", ""))
}

func (h Handler) createConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The channel could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	private := strings.EqualFold(strings.TrimSpace(fields["is_private"]), "true")
	conversation, err := h.Messages.CreateConversation(r.Context(), principal.WorkspaceID, principal.UserID, fields["name"], private)
	if err != nil {
		status := http.StatusServiceUnavailable
		heading := "The channel was not created"
		reason := "The channel could not be created because the workspace store is temporarily unavailable."
		switch {
		case errors.Is(err, service.ErrInvalidConversation):
			status = http.StatusBadRequest
			reason = "Use a channel name between one and 80 characters."
		case errors.Is(err, store.ErrAlreadyExists):
			status = http.StatusConflict
			reason = "A channel with that name already exists."
		}
		h.writeMutationError(w, r, status, heading, reason)
		return
	}
	h.redirectMutation(w, r, appURL(string(conversation.ID), "", "", "", ""))
}

func (h Handler) inviteConversationMember(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The member invitation could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	target := domain.UserID(strings.TrimSpace(fields["user"]))
	if target == "" {
		h.writeMutationError(w, r, http.StatusBadRequest, "Choose a person to add", "No workspace member was selected, so nobody was added.")
		return
	}
	if _, err := h.Messages.InviteConversationMembers(r.Context(), principal.WorkspaceID, principal.UserID, channel, []domain.UserID{target}); err != nil {
		switch {
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this channel", "Join the channel before inviting another person.")
		case errors.Is(err, service.ErrInvalidConversation):
			h.writeMutationError(w, r, http.StatusBadRequest, "That person cannot be added here", "Members can be added to public and private channels, not direct conversations.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That person is no longer available", "The member or channel no longer exists.")
		case errors.Is(err, store.ErrAlreadyExists):
			h.writeMutationError(w, r, http.StatusConflict, "That person is already in this channel", "No duplicate invitation was created.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The person was not added", "The workspace store is temporarily unavailable. Nothing was changed.")
		}
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel))
}

func (h Handler) renameConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The channel name could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	if _, err := h.Messages.RenameConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields["name"]); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidConversation):
			h.writeMutationError(w, r, http.StatusBadRequest, "That channel name is not valid", "Use a unique channel name between one and 80 characters.")
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this channel", "Only a channel member can rename it.")
		case errors.Is(err, store.ErrAlreadyExists):
			h.writeMutationError(w, r, http.StatusConflict, "That channel name is already in use", "Choose another name.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That channel is no longer available", "Nothing was renamed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The channel was not renamed", "The workspace store is temporarily unavailable. Nothing was changed.")
		}
		return
	}
	if r.URL.Query().Get("return") == "dms" {
		h.redirectMutation(w, r, "/app/dms")
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel))
}

func (h Handler) setConversationTopic(w http.ResponseWriter, r *http.Request) {
	h.setConversationText(w, r, "topic")
}

func (h Handler) setConversationPurpose(w http.ResponseWriter, r *http.Request) {
	h.setConversationText(w, r, "purpose")
}

func (h Handler) setConversationNotifications(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The notification exception could not be read. Reload the conversation and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	if _, err := h.Messages.SetConversationNotificationPreferences(
		r.Context(), principal.WorkspaceID, principal.UserID, channel,
		domain.NotificationLevel(strings.TrimSpace(fields["level"])),
		fields["follow_every_thread"] == "true",
	); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalidArgument):
			h.writeMutationError(w, r, http.StatusBadRequest, "That notification exception is not valid", "Choose the workspace default, all new posts, mentions, or mute.")
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this channel", "Only channel members can change its notification exception.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That channel is no longer available", "Nothing was changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The notification exception was not saved", "The workspace store is temporarily unavailable.")
		}
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel)+"#conversation-notifications")
}

func (h Handler) setThreadFollow(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsHistory)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The thread follow action could not be read. Reload the thread and try again.")
	if !ok {
		return
	}
	followed, err := strconv.ParseBool(strings.TrimSpace(fields["followed"]))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That thread follow action is not valid", "Nothing was changed.")
		return
	}
	threadValue := strings.TrimSpace(r.URL.Query().Get("thread_ts"))
	if threadValue == "" {
		threadValue = strings.TrimSpace(r.URL.Query().Get("thread"))
	}
	thread := domain.MessageTimestamp(threadValue)
	if err := h.Messages.SetThreadFollowed(
		r.Context(), principal.WorkspaceID, principal.UserID, h.requestChannel(r), thread, followed,
	); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidTimestamp), errors.Is(err, store.ErrInvalidArgument):
			h.writeMutationError(w, r, http.StatusBadRequest, "That thread link is not valid", "Nothing was changed.")
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this channel", "Only channel members can follow its threads.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That thread is no longer available", "Nothing was changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The thread follow state was not saved", "The workspace store is temporarily unavailable.")
		}
		return
	}
	h.redirectMutation(w, r, appURL(string(h.requestChannel(r)), string(thread), "", "", "")+"#thread-heading")
}

func (h Handler) setConversationText(w http.ResponseWriter, r *http.Request, field string) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The channel details could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	channel := h.requestChannel(r)
	if field == "topic" {
		_, err = h.Messages.SetConversationTopic(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields[field])
	} else {
		_, err = h.Messages.SetConversationPurpose(r.Context(), principal.WorkspaceID, principal.UserID, channel, fields[field])
	}
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidConversation):
			h.writeMutationError(w, r, http.StatusBadRequest, "That channel "+field+" is too long", "Use at most 250 characters.")
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusForbidden, "You are not a member of this conversation", "Only a conversation member can change its "+field+".")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That conversation is no longer available", "Nothing was changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The "+field+" was not saved", "The workspace store is temporarily unavailable. Nothing was changed.")
		}
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel))
}

func (h Handler) setConversationArchived(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	fields, ok := h.decodeMutation(w, r, "The archive request could not be read from the form. Reload the page and try again.")
	if !ok {
		return
	}
	archived, err := strconv.ParseBool(strings.TrimSpace(fields["archived"]))
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That archive request is not valid", "Nothing was changed.")
		return
	}
	channel := h.requestChannel(r)
	if _, err := h.Messages.SetConversationArchived(r.Context(), principal.WorkspaceID, principal.UserID, channel, archived); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidConversation), errors.Is(err, service.ErrCannotArchiveDefault):
			h.writeMutationError(w, r, http.StatusBadRequest, "This conversation cannot be archived", "Only public and private channels that are not required by the workspace can be archived.")
		case errors.Is(err, service.ErrConversationAlreadyArchived), errors.Is(err, service.ErrConversationNotArchived):
			h.redirectMutation(w, r, conversationDetailsURL(channel))
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That channel is no longer available", "Nothing was changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The channel archive state was not changed", "The workspace store is temporarily unavailable. Nothing was changed.")
		}
		return
	}
	h.redirectMutation(w, r, conversationDetailsURL(channel))
}

func (h Handler) leaveConversation(w http.ResponseWriter, r *http.Request) {
	principal, err := h.authenticate(r, auth.ScopeChannelsManage)
	if err != nil {
		h.writeAuthError(w, r, err)
		return
	}
	if _, ok := h.decodeMutation(w, r, "The leave request could not be read from the form. Reload the page and try again."); !ok {
		return
	}
	channel := h.requestChannel(r)
	conversation, infoErr := h.Messages.ConversationInfo(r.Context(), principal.WorkspaceID, principal.UserID, channel)
	if infoErr != nil {
		h.writeMutationError(w, r, http.StatusNotFound, "That conversation is no longer available", "Nothing was changed.")
		return
	}
	if err := h.Messages.LeaveConversation(r.Context(), principal.WorkspaceID, principal.UserID, channel); err != nil {
		switch {
		case errors.Is(err, store.ErrAlreadyExists) && (conversation.IsDirectOrGroup()):
			h.redirectMutation(w, r, "/app/dms")
		case errors.Is(err, service.ErrNotInConversation):
			h.writeMutationError(w, r, http.StatusConflict, "You have already left this conversation", "No membership was changed.")
		case errors.Is(err, service.ErrInvalidConversation), errors.Is(err, service.ErrCannotLeaveDefault):
			h.writeMutationError(w, r, http.StatusBadRequest, "This conversation cannot be left", "Required workspace channels cannot be left.")
		case errors.Is(err, store.ErrNotFound):
			h.writeMutationError(w, r, http.StatusNotFound, "That conversation is no longer available", "Nothing was changed.")
		default:
			h.writeMutationError(w, r, http.StatusServiceUnavailable, "The conversation was not left", "The workspace store is temporarily unavailable. Your membership was not changed.")
		}
		return
	}
	if conversation.IsDirectOrGroup() {
		h.redirectMutation(w, r, "/app/dms")
		return
	}
	h.redirectMutation(w, r, appURL(string(channel), "", "", "", ""))
}

func conversationDetailsURL(channel domain.ConversationID) string {
	return "/app?" + url.Values{"channel": {string(channel)}, "details": {"1"}}.Encode()
}

func normalizeUserIDs(raw string) ([]domain.UserID, error) {
	parts := strings.Split(raw, ",")
	users := make([]domain.UserID, 0, len(parts))
	seen := make(map[domain.UserID]struct{}, len(parts))
	for _, part := range parts {
		user := domain.UserID(strings.TrimSpace(part))
		if user == "" {
			return nil, errors.New("conversation user is empty")
		}
		if _, exists := seen[user]; exists {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	if len(users) == 0 {
		return nil, errors.New("conversation requires a user")
	}
	return users, nil
}

// completeMutation answers a state change that has no fragment of its own. The
// enhanced client re-renders the live message regions on 204, so the reader
// keeps their draft, their scroll position and the thread they had open; a
// browser without JavaScript returns to exactly the view it submitted from.
func (h Handler) completeMutation(w http.ResponseWriter, r *http.Request) {
	// The response shape is chosen by a request header, so a cache that keeps
	// one must not serve it to the other.
	w.Header().Set("Vary", "HX-Request")
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, h.viewURL(r, strings.TrimSpace(r.URL.Query().Get("thread"))), http.StatusSeeOther)
}

func (h Handler) viewURL(r *http.Request, thread string) string {
	return appURL(string(h.requestChannel(r)), thread, strings.TrimSpace(r.URL.Query().Get("before")), "", "")
}

func (h Handler) requestChannel(r *http.Request) domain.ConversationID {
	if channel := strings.TrimSpace(r.URL.Query().Get("channel")); channel != "" {
		return domain.ConversationID(channel)
	}
	return h.Channel
}

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

// workspaceContentSecurityPolicy is the workspace equivalent of the policy the
// administration page already carried. It differs in exactly four places, each
// of which the workspace page actually needs: the hashes of its own inline
// scripts (so an injected script still cannot run), `connect-src 'self'` for
// the fragment fetches and the event stream, `img-src` for profile images, and
// no `form-action`.
//
// form-action is omitted deliberately. Sign-out posts to this origin and is
// answered with a redirect to the identity provider's end-session endpoint, and
// browsers disagree about whether form-action applies across that redirect: the
// directive would leave global sign-out working in one browser and broken in
// another. The administration page keeps it, because every form there redirects
// to itself.
var workspaceContentSecurityPolicy = "default-src 'none'; script-src " +
	strings.Join(inlineScriptHashes(themeBootstrap, themeToggleScript, progressiveEnhancementScript, huddleMediaScript, searchSuggestionsScript, developerAppsScript, appOptionsScript, laterLiveScript, activityMarkup, draftsAndSentMarkup, membersMarkup, workflowsMarkup, workflowMarkup, workflowRunMarkup), " ") +
	"; style-src 'unsafe-inline'; img-src 'self' https: data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// entryContentSecurityPolicy covers the two pages a signed-out visitor reaches:
// /login and /signed-out. Both are static documents with one inline stylesheet
// and no script, so the policy can be stricter than the workspace's. They used
// to carry no policy and no X-Frame-Options at all, which made both framable —
// and a provider-backed /signed-out page carries a sign-in link, so framing it
// is enough to make a victim start an authorization flow they did not choose.
const entryContentSecurityPolicy = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"

// inlineScriptHashes is the CSP source list for a set of documents' inline
// scripts. The scripts are package constants with no template action inside
// them, so their bytes are known at initialisation and a hash is exact.
func inlineScriptHashes(documents ...string) []string {
	hashes := make([]string, 0, len(documents))
	seen := map[string]struct{}{}
	for _, document := range documents {
		for _, body := range inlineScriptBodies(document) {
			digest := sha256.Sum256([]byte(body))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if _, repeated := seen[hash]; repeated {
				continue
			}
			seen[hash] = struct{}{}
			hashes = append(hashes, hash)
		}
	}
	return hashes
}

// inlineScriptBodies returns the body of every inline script in a document.
//
// It refuses any element it cannot hash rather than skipping it. Searching for
// the literal "<script>" made `<script type="module">` or `<script defer>`
// invisible to the policy builder *and* to the test that checks the policy
// against the document, so the browser would block a script both agreed was
// covered. A tag this cannot hash is a build-time panic, which is the same
// failure the package already takes for an unclosed script.
func inlineScriptBodies(document string) []string {
	const marker, open, close = "<script", "<script>", "</script>"
	bodies := make([]string, 0, 2)
	for {
		start := strings.Index(document, marker)
		if start < 0 {
			return bodies
		}
		if !strings.HasPrefix(document[start:], open) {
			panic("inline script carries attributes, so its hash cannot be computed: " + document[start:min(start+40, len(document))])
		}
		document = document[start+len(open):]
		end := strings.Index(document, close)
		if end < 0 {
			panic("inline script is not closed")
		}
		if strings.Contains(document[:end], marker) {
			panic("inline script body contains a nested script tag")
		}
		bodies = append(bodies, document[:end])
		document = document[end+len(close):]
	}
}

// secureHeaders is the header set every authenticated response in this package
// carries. Without it the workspace was framable — and framing is enough to
// turn one click into Sign out, Pin, or "Message <attacker>", because the
// framed page supplies its own valid token — and an authenticated page holding
// a conversation and a live CSRF token was stored by the browser, so Back after
// sign-out replayed it.
func secureHeaders(w http.ResponseWriter, policy string) {
	header := w.Header()
	header.Set("Content-Security-Policy", policy)
	header.Set("X-Frame-Options", "DENY")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
}

func (h Handler) writeHTML(w http.ResponseWriter, page *template.Template, data any, status int, unavailable string) {
	h.writeHTMLWithPolicy(w, page, data, status, unavailable, workspaceContentSecurityPolicy)
}

// writeHTMLWithPolicy serves a page under a policy of its own. A page outside
// the workspace shell carries a different set of inline scripts and a different
// set of things it is allowed to do, and serving it under the workspace policy
// would either allow more than it needs or block the scripts it has.
func (h Handler) writeHTMLWithPolicy(w http.ResponseWriter, page *template.Template, data any, status int, unavailable, policy string) {
	var output bytes.Buffer
	if err := page.Execute(&output, data); err != nil {
		secureHeaders(w, policy)
		http.Error(w, unavailable, http.StatusServiceUnavailable)
		return
	}
	secureHeaders(w, policy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (h Handler) writeFragment(w http.ResponseWriter, list messageList) {
	h.writePartial(w, "messages", list, "the conversation could not be rendered")
}

// writePartial renders one named partial of the workspace page as a fragment
// response. Every live region the client refreshes goes through it, so they
// cannot disagree about the policy or the content type.
func (h Handler) writePartial(w http.ResponseWriter, name string, data any, unavailable string) {
	var output bytes.Buffer
	if err := pageTemplate.ExecuteTemplate(&output, name, data); err != nil {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, unavailable, http.StatusServiceUnavailable)
		return
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(output.Bytes())
}

func (h Handler) writePageError(w http.ResponseWriter, status int, heading, message string) {
	var output bytes.Buffer
	if err := errorTemplate.Execute(&output, errorData{Heading: heading, Message: message}); err != nil {
		secureHeaders(w, workspaceContentSecurityPolicy)
		http.Error(w, heading, status)
		return
	}
	secureHeaders(w, workspaceContentSecurityPolicy)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

// writeStoreError separates a missing or forbidden conversation from an outage:
// a stale bookmark is a 404 page, not a 503 that blames the store.
func (h Handler) writeStoreError(w http.ResponseWriter, err error, unavailable string) {
	if errors.Is(err, store.ErrNotFound) {
		h.writePageError(w, http.StatusNotFound, "That conversation is not available", "It may have been deleted, or you may not be a member of it. Pick a conversation from the sidebar to keep reading.")
		return
	}
	h.writePageError(w, http.StatusServiceUnavailable, "Temporarily unavailable", unavailable)
}

// writeFragmentError and writeAuthError answer with a bare line of text, which
// is what the enhanced client can render next to the control that failed. The
// bytes are still an authenticated response on this origin, so they carry the
// same five headers as every rendered page: without them a fragment failure and
// every insufficient-scope answer were framable, sniffable and stored by the
// browser, while the test that checks the header set only ever visited a
// successful page and a 404.
func (h Handler) writeFragmentError(w http.ResponseWriter, err error, unavailable string) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "that conversation is not available", http.StatusNotFound)
		return
	}
	http.Error(w, unavailable, http.StatusServiceUnavailable)
}

func (h Handler) authenticate(r *http.Request, scope auth.Scope) (auth.Principal, error) {
	principal, err := h.Authenticator.Authenticate(r)
	if err != nil {
		return auth.Principal{}, err
	}
	if !principal.HasScope(scope) {
		return auth.Principal{}, auth.ErrMissingScope
	}
	return principal, nil
}

func (h Handler) writeAuthError(w http.ResponseWriter, r *http.Request, err error) {
	secureHeaders(w, workspaceContentSecurityPolicy)
	// A credential store that did not answer is server trouble, not an
	// authentication outcome: nothing is known about the session, so neither a
	// 401 nor a login redirect is honest, and both push a signed-in person
	// back through sign-in for a transient backend failure.
	if errors.Is(err, auth.ErrCredentialStoreUnavailable) {
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if errors.Is(err, auth.ErrMissingScope) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// A person navigating to a page without a session belongs on the sign-in
	// flow. Only GET /app used to redirect, so every deeper page — /app/members,
	// /app/later, a link a teammate shared — answered a bare text 401 to a
	// browser. Fragment fetches (the app's own requests, marked HX-Request)
	// keep the 401: redirecting them would inject the sign-in page into a
	// fragment swap instead of navigating.
	if errors.Is(err, auth.ErrNotAuthenticated) && r.Method == http.MethodGet && r.Header.Get("HX-Request") == "" {
		http.Redirect(w, r, h.signInTarget(r)+"?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
		return
	}
	http.Error(w, "not authenticated", http.StatusUnauthorized)
}

// writeJSONAuthError refuses a request to a route whose answers are JSON.
//
// writeAuthError above is built for pages, and its GET branch answers 303 to
// the sign-in page. fetch follows a redirect transparently, so a script asking
// a JSON route for data got the sign-in page back with status 200: the guard
// clause every one of these callers has — "if the response is not ok, give
// up" — never fired, and the failure surfaced one step later as a JSON parse
// error. The member was told the emoji, or the suggestions, could not be
// loaded, when what had actually happened was that their session ended and
// signing in again would have fixed it.
//
// Routing every JSON refusal through one function is what keeps the page
// redirect out of them: a JSON route does not reach writeAuthError at all.
func writeJSONAuthError(w http.ResponseWriter, err error) {
	writeJSONRefusal(w, jsonAuthStatus(err), "not_authenticated")
}

// jsonAuthStatus keeps the distinctions writeAuthError draws, because they are
// the difference between "sign in again" and "this will work in a moment": a
// credential store that did not answer says nothing about the session, and
// answering 401 to it sends a signed-in member back through sign-in for a
// transient backend failure.
func jsonAuthStatus(err error) int {
	switch {
	case errors.Is(err, auth.ErrCredentialStoreUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, auth.ErrMissingScope):
		return http.StatusForbidden
	default:
		return http.StatusUnauthorized
	}
}

// writeJSONRefusal is the one refusal shape the JSON routes answer. The code
// is a fixed identifier chosen by the caller, never anything derived from the
// request, so there is nothing to escape.
func writeJSONRefusal(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"ok":false,"error":"` + code + `"}`))
}

// userNames resolves author display names once per request. Message authors are
// people, and the timeline shows their names; a raw user identifier is only the
// last resort for a record that carries no name at all.
type userNames struct {
	handler      Handler
	ctx          context.Context
	principal    auth.Principal
	cache        map[domain.UserID]userNameEntry
	channels     map[domain.ConversationID]string
	groups       map[domain.UserGroupID]string
	groupsLoaded bool
}

// userNameEntry is everything a caller resolves about a member from the single
// UserInfo lookup: the display name every caller needs, and the status a caller
// projecting a member's presence next to their name needs. Caching the whole
// entry means the status projection costs no extra store round-trip beyond the
// name resolution the timeline already does.
type userNameEntry struct {
	name        string
	statusEmoji string
	statusText  string
}

func (h Handler) newUserNames(ctx context.Context, principal auth.Principal) *userNames {
	return &userNames{
		handler: h, ctx: ctx, principal: principal,
		cache: map[domain.UserID]userNameEntry{}, channels: map[domain.ConversationID]string{}, groups: map[domain.UserGroupID]string{},
	}
}

func (n *userNames) channelName(id domain.ConversationID) string {
	if cached, ok := n.channels[id]; ok {
		return cached
	}
	resolved := "private channel"
	if conversation, err := n.handler.Messages.ConversationInfo(n.ctx, n.principal.WorkspaceID, n.principal.UserID, id); err == nil {
		resolved = "#" + conversationName(conversation)
	}
	n.channels[id] = resolved
	return resolved
}

func (n *userNames) name(id domain.UserID) string {
	return n.entry(id).name
}

// statusEmoji is the member's status emoji shortcode (":wave:") or empty, and
// statusText its accompanying prose. Both come from the same cached lookup name
// resolution uses, so projecting a member's status beside their name is free
// once their name has been resolved.
func (n *userNames) statusEmoji(id domain.UserID) string { return n.entry(id).statusEmoji }
func (n *userNames) statusText(id domain.UserID) string  { return n.entry(id).statusText }

func (n *userNames) entry(id domain.UserID) userNameEntry {
	if id == "" {
		return userNameEntry{name: "Unknown member"}
	}
	if cached, ok := n.cache[id]; ok {
		return cached
	}
	entry := userNameEntry{name: string(id)}
	if user, err := n.handler.Messages.UserInfo(n.ctx, n.principal.WorkspaceID, n.principal.UserID, id); err == nil {
		entry.name = displayName(user)
		entry.statusEmoji = user.Profile.StatusEmoji
		entry.statusText = user.Profile.StatusText
	}
	n.cache[id] = entry
	return entry
}

func (n *userNames) groupHandle(id domain.UserGroupID) string {
	if !n.groupsLoaded {
		cursor := domain.Cursor("")
		seen := make(map[domain.Cursor]struct{})
		for {
			if _, repeated := seen[cursor]; repeated {
				break
			}
			seen[cursor] = struct{}{}
			page, err := n.handler.Messages.ListUserGroups(n.ctx, n.principal.WorkspaceID, n.principal.UserID, true, domain.PageRequest{Limit: memberWindow, Cursor: cursor})
			if err != nil {
				break
			}
			for _, group := range page.Groups {
				n.groups[group.ID] = strings.TrimSpace(group.Handle)
			}
			if !page.HasMore || page.NextCursor == "" || page.NextCursor == cursor {
				break
			}
			cursor = page.NextCursor
		}
		n.groupsLoaded = true
	}
	return n.groups[id]
}

// resolveSlackUserMentions adds a human label to bare Slack user references for
// first-party presentation without changing the stored message. References
// inside code spans and fenced code blocks stay literal, as Slack rendering
// requires.
func resolveSlackUserMentions(text string, names *userNames) string {
	return resolveSlackReferences(text, names)
}

func resolveSlackReferences(text string, names *userNames) string {
	if (!strings.Contains(text, "<@") && !strings.Contains(text, "<#") && !strings.Contains(text, "<!subteam^")) || names == nil {
		return text
	}
	var output strings.Builder
	for offset := 0; offset < len(text); {
		if strings.HasPrefix(text[offset:], "```") {
			end := strings.Index(text[offset+3:], "```")
			if end < 0 {
				output.WriteString(text[offset:])
				break
			}
			end += offset + 6
			output.WriteString(text[offset:end])
			offset = end
			continue
		}
		if text[offset] == '`' {
			end := strings.IndexByte(text[offset+1:], '`')
			if end < 0 {
				output.WriteString(text[offset:])
				break
			}
			end += offset + 2
			output.WriteString(text[offset:end])
			offset = end
			continue
		}
		if strings.HasPrefix(text[offset:], "<@") {
			end := strings.IndexByte(text[offset+2:], '>')
			if end >= 0 {
				end += offset + 2
				id := text[offset+2 : end]
				if id != "" && !strings.ContainsAny(id, "| \t\r\n<>") {
					label := strings.Join(strings.Fields(strings.NewReplacer("|", " ", "<", " ", ">", " ").Replace(names.name(domain.UserID(id)))), " ")
					if label != "" {
						output.WriteString("<@")
						output.WriteString(id)
						output.WriteByte('|')
						output.WriteByte('@')
						output.WriteString(label)
						output.WriteByte('>')
						offset = end + 1
						continue
					}
				}
			}
		}
		if strings.HasPrefix(text[offset:], "<!subteam^") {
			end := strings.IndexByte(text[offset+len("<!subteam^"):], '>')
			if end >= 0 {
				end += offset + len("<!subteam^")
				raw := text[offset+len("<!subteam^") : end]
				id, _, _ := strings.Cut(raw, "|")
				if id != "" && !strings.ContainsAny(id, " \t\r\n<>") {
					handle := strings.Join(strings.Fields(strings.NewReplacer("|", " ", "<", " ", ">", " ").Replace(names.groupHandle(domain.UserGroupID(id)))), " ")
					if handle != "" {
						output.WriteString("<!subteam^")
						output.WriteString(id)
						output.WriteString("|@")
						output.WriteString(handle)
						output.WriteByte('>')
						offset = end + 1
						continue
					}
				}
			}
		}
		if strings.HasPrefix(text[offset:], "<#") {
			end := strings.IndexByte(text[offset+2:], '>')
			if end >= 0 {
				end += offset + 2
				id := text[offset+2 : end]
				if id != "" && !strings.ContainsAny(id, "| \t\r\n<>") {
					label := strings.Join(strings.Fields(strings.NewReplacer("|", " ", "<", " ", ">", " ").Replace(names.channelName(domain.ConversationID(id)))), " ")
					if label != "" {
						output.WriteString("<#")
						output.WriteString(id)
						output.WriteByte('|')
						output.WriteString(label)
						output.WriteByte('>')
						offset = end + 1
						continue
					}
				}
			}
		}
		output.WriteByte(text[offset])
		offset++
	}
	return output.String()
}

// resolveSlackReferenceJSON decorates only Slack text-bearing JSON fields. It
// leaves identifiers, action values and URLs byte-for-byte equivalent after
// decoding so a mention-like substring in an opaque value cannot change an
// interaction target. Rich-text usergroup elements carry their resolved handle
// separately for the renderer.
func resolveSlackReferenceJSON(raw string, names *userNames) string {
	if names == nil || (!strings.Contains(raw, "<@") && !strings.Contains(raw, "<#") && !strings.Contains(raw, "<!subteam^") && !strings.Contains(raw, `"usergroup"`)) {
		return raw
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return raw
	}
	var resolve func(any) any
	resolve = func(current any) any {
		switch typed := current.(type) {
		case []any:
			for index := range typed {
				typed[index] = resolve(typed[index])
			}
		case map[string]any:
			kind, _ := typed["type"].(string)
			if kind == "usergroup" {
				if id, ok := typed["usergroup_id"].(string); ok {
					typed["display_handle"] = names.groupHandle(domain.UserGroupID(id))
				}
			}
			_, hasAttachmentFieldTitle := typed["title"]
			_, hasAttachmentFieldShort := typed["short"]
			attachmentField := kind == "" && (hasAttachmentFieldTitle || hasAttachmentFieldShort)
			for key, child := range typed {
				text, textValue := child.(string)
				mrkdwnText := textValue && key == "text" && (kind == "mrkdwn" || kind == "markdown")
				legacyAttachmentText := textValue && kind == "" && (key == "text" || key == "pretext" || (key == "value" && attachmentField))
				if mrkdwnText || legacyAttachmentText {
					typed[key] = resolveSlackReferences(text, names)
					continue
				}
				typed[key] = resolve(child)
			}
		}
		return current
	}
	encoded, err := json.Marshal(resolve(value))
	if err != nil {
		return raw
	}
	return string(encoded)
}

func displayName(user domain.User) string {
	for _, candidate := range []string{user.Profile.DisplayName, user.RealName, user.Name} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return string(user.ID)
}

// profileImageURL chooses the best single image for a human-facing profile
// form. Slack's API carries size-specific URLs, but asking a person to edit
// seven storage variants exposed transport detail as product UI.
func profileImageURL(profile domain.UserProfile) string {
	for _, candidate := range []string{profile.Image192, profile.Image72, profile.Image48, profile.Image512, profile.Image1024, profile.Image32, profile.Image24} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func webUnixSeconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().Unix()
}

// webPresence never turns another member's stored automatic mode into evidence
// that they are online. The current request is activity evidence for the signed
// in member; everyone else remains neutral until session activity tracking can
// derive Slack's effective active/away state.
func webPresence(value domain.Presence, isSelf bool) string {
	if value == domain.PresenceAway {
		return "away"
	}
	if isSelf {
		return "active"
	}
	return "auto"
}

func conversationName(conversation domain.Conversation) string {
	if name := strings.TrimSpace(conversation.Name); name != "" {
		return name
	}
	return string(conversation.ID)
}

func conversationMeta(conversation domain.Conversation) string {
	if topic := strings.TrimSpace(conversation.Topic); topic != "" {
		return topic
	}
	if purpose := strings.TrimSpace(conversation.Purpose); purpose != "" {
		return purpose
	}
	if conversation.IsDirectOrGroup() {
		return "Direct message"
	}
	if conversation.Kind == domain.ConversationTypePrivate {
		return "Private channel"
	}
	return "Channel"
}

// initial is the single uppercase letter an avatar tile can hold. The tile is
// 24 to 72 pixels wide; a name or an identifier does not fit in it.
func initial(name string) string {
	for _, character := range strings.TrimSpace(name) {
		return strings.ToUpper(string(character))
	}
	return "?"
}

func messageAnchor(id domain.MessageID) string {
	return "message-" + string(id)
}

func formatTime(value time.Time) string {
	return value.UTC().Format("Jan 2, 15:04 UTC")
}

func appURL(channel, thread, before, anchor, conversations string) string {
	query := url.Values{"channel": {channel}}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	if conversations != "" {
		query.Set("conversations", conversations)
	}
	result := "/app?" + query.Encode()
	if anchor != "" {
		result += "#" + anchor
	}
	return result
}

func mutationURL(path, channel, timestamp, thread, before string) string {
	query := url.Values{"channel": {channel}}
	if timestamp != "" {
		query.Set("ts", timestamp)
	}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	return path + "?" + query.Encode()
}

func fragmentURL(channel, thread, before string) string {
	query := url.Values{"channel": {channel}}
	if thread != "" {
		query.Set("thread", thread)
	}
	if before != "" {
		query.Set("before", before)
	}
	return "/app/timeline?" + query.Encode()
}

// ---------------------------------------------------------------------------
// Form decoding
// ---------------------------------------------------------------------------

const maxFormBody = 4 << 20

// decodeMutation bounds and parses the body before anything reads a form value.
// CSRF validation falls back to the token form field, and reading that field
// parses the whole body: validating first would let net/http buffer its own 32
// MB default and leave maxFormBody unreachable.
func (h Handler) decodeMutation(w http.ResponseWriter, r *http.Request, invalid string) (map[string]string, bool) {
	fields, err := decodeFormFields(w, r)
	if err != nil {
		h.writeMutationError(w, r, http.StatusBadRequest, "That form could not be read", invalid)
		return nil, false
	}
	if !h.requireCSRF(w, r) {
		return nil, false
	}
	return fields, true
}

// decodeFormValues is decodeFormFields for a form with exactly one field the
// caller expects to repeat — a set of checkboxes sharing a name. Every other
// field must still occur once: the single-occurrence rule exists so a request
// cannot carry two different values for one field and leave the reader to pick,
// and naming the repeating field keeps that guarantee for all the rest.
func decodeFormValues(w http.ResponseWriter, r *http.Request, repeated string) (map[string]string, []string, error) {
	fields, err := decodeFormFieldsAllowing(w, r, repeated)
	if err != nil {
		return nil, nil, err
	}
	values := append([]string(nil), r.Form[repeated]...)
	return fields, values, nil
}

func decodeFormFields(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	return decodeFormFieldsAllowing(w, r, "")
}

func decodeFormFieldsAllowing(w http.ResponseWriter, r *http.Request, repeated string) (map[string]string, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBody)
	var parseErr error
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
		parseErr = r.ParseMultipartForm(maxFormBody)
	} else {
		parseErr = r.ParseForm()
	}
	if parseErr != nil {
		return nil, parseErr
	}
	fields := make(map[string]string, len(r.Form))
	for name, values := range r.Form {
		if name == repeated && repeated != "" {
			fields[name] = strings.Join(values, ",")
			continue
		}
		if len(values) != 1 {
			return nil, errors.New("form fields must occur once")
		}
		fields[name] = values[0]
	}
	return fields, nil
}
