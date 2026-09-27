package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// callback is one Events API request an app endpoint received.
type callback struct {
	EventID     string
	Type        string
	User        string
	Text        string
	ChannelType string
	AuthUser    string
	RetryNum    string
	RetryReason string
}

// appEndpoint is a TLS Events API receiver whose answer to each callback is
// decided by respond, and which records every callback it answered.
type appEndpoint struct {
	server    *httptest.Server
	mutex     sync.Mutex
	callbacks []callback
}

func newAppEndpoint(t *testing.T, respond func(callback) int) *appEndpoint {
	t.Helper()
	endpoint := &appEndpoint{}
	endpoint.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var envelope struct {
			Type           string `json:"type"`
			Challenge      string `json:"challenge"`
			EventID        string `json:"event_id"`
			Authorizations []struct {
				UserID string `json:"user_id"`
			} `json:"authorizations"`
			Event struct {
				Type        string `json:"type"`
				User        string `json:"user"`
				Text        string `json:"text"`
				ChannelType string `json:"channel_type"`
			} `json:"event"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Error(err)
			return
		}
		if envelope.Type == "url_verification" {
			_, _ = io.WriteString(w, envelope.Challenge)
			return
		}
		received := callback{
			EventID: envelope.EventID, Type: envelope.Event.Type, User: envelope.Event.User, Text: envelope.Event.Text,
			ChannelType: envelope.Event.ChannelType,
			RetryNum:    r.Header.Get("X-Slack-Retry-Num"), RetryReason: r.Header.Get("X-Slack-Retry-Reason"),
		}
		if len(envelope.Authorizations) != 0 {
			received.AuthUser = envelope.Authorizations[0].UserID
		}
		status := respond(received)
		endpoint.mutex.Lock()
		endpoint.callbacks = append(endpoint.callbacks, received)
		endpoint.mutex.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(endpoint.server.Close)
	return endpoint
}

func (e *appEndpoint) received() []callback {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return append([]callback(nil), e.callbacks...)
}

// deliveryWorkspace is a workspace with one human member, U1, and a place for
// installed apps whose bots are distinct users.
type deliveryWorkspace struct {
	repository *memory.Store
	messages   service.Messages
	key        []byte
}

func newDeliveryWorkspace(t *testing.T) *deliveryWorkspace {
	t.Helper()
	repository := memory.New()
	repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"})
	repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	key := []byte(strings.Repeat("k", 32))
	return &deliveryWorkspace{repository: repository, messages: service.Messages{Store: repository, AppCredentialKey: key}, key: key}
}

// installApp creates and installs an app subscribed to botEvents at
// endpoint, with a bot user botUser holding scopes, and returns its ID.
func (w *deliveryWorkspace) installApp(t *testing.T, endpoint *appEndpoint, botUser domain.UserID, botEvents, scopes []string) domain.AppID {
	t.Helper()
	ctx := context.Background()
	// Creating the app verifies its request URL, so the service must trust
	// the endpoint's test certificate.
	w.messages.AppHTTPClient = endpoint.server.Client()
	configuration, err := w.messages.IssueAppConfigurationToken(ctx, "T1", "U1")
	if err != nil {
		t.Fatal(err)
	}
	encodedEvents, _ := json.Marshal(botEvents)
	encodedScopes, _ := json.Marshal(scopes)
	manifest := `{"display_information":{"name":"Delivery ` + string(botUser) + `"},"oauth_config":{"scopes":{"bot":` + string(encodedScopes) +
		`}},"settings":{"event_subscriptions":{"request_url":"` + endpoint.server.URL + `","bot_events":` + string(encodedEvents) + `}}}`
	app, _, err := w.messages.CreateAppFromManifest(ctx, configuration.Token, manifest, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: app.ID, WorkspaceID: "T1", Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w.repository.SeedUser(domain.User{ID: botUser, WorkspaceID: "T1", Name: strings.ToLower(string(botUser))})
	botID := domain.BotID("B" + string(botUser))
	if err := w.repository.CreateBot(ctx, domain.Bot{ID: botID, WorkspaceID: "T1", AppID: app.ID, UserID: botUser, Name: string(botUser), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := w.repository.SeedToken(ctx, "xoxb-"+string(botUser), domain.TokenRecord{WorkspaceID: "T1", UserID: botUser, AppID: app.ID, BotID: botID, TokenType: "bot", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	return app.ID
}

func (w *deliveryWorkspace) processor(client *http.Client) EventProcessor {
	// A clock in the past makes every retry the processor schedules due at
	// once, so a test needs no waiting; the store's own clock is real time.
	return EventProcessor{
		Store: w.repository, AppCredentialKey: w.key, Owner: "worker-1", Lease: time.Minute, Client: client,
		Now: func() time.Time { return time.Now().UTC().Add(-time.Hour) },
	}
}

// A person's direct message to an app's bot reaches the app as a message
// event with channel_type "im" — what Bolt's Assistant middleware requires —
// authored by the person and authorized as the bot, which is a different user.
func TestHTTPDeliveryOfADirectMessageNamesThePersonTheBotAndTheIMChannel(t *testing.T) {
	ctx := context.Background()
	workspace := newDeliveryWorkspace(t)
	endpoint := newAppEndpoint(t, func(callback) int { return http.StatusOK })
	workspace.installApp(t, endpoint, "UBOT", []string{"message.im"}, []string{"im:history", "chat:write"})
	workspace.repository.SeedConversation(domain.Conversation{ID: "D1", WorkspaceID: "T1", Name: "direct", Kind: domain.ConversationTypeIM})
	workspace.repository.SeedConversationMember("D1", "U1")
	workspace.repository.SeedConversationMember("D1", "UBOT")
	if _, err := workspace.messages.Post(ctx, "T1", "U1", "D1", "hello assistant", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.processor(endpoint.server.Client()).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	received := endpoint.received()
	if len(received) != 1 {
		t.Fatalf("callbacks=%+v, want the one message", received)
	}
	got := received[0]
	if got.Type != "message" || got.ChannelType != "im" || got.User != "U1" || got.AuthUser != "UBOT" || got.Text != "hello assistant" {
		t.Fatalf("callback=%+v, want a message in an im from U1 authorized as UBOT", got)
	}
}

// One callback an app fails must not hold back the app's later events: they
// are delivered in the same cycle while the failed one waits for its own
// retry. The delivery state used to be one position per app, so a failure
// parked every later event for the length of Slack's retry schedule.
func TestAFailingCallbackDoesNotHoldBackTheAppsLaterEvents(t *testing.T) {
	ctx := context.Background()
	workspace := newDeliveryWorkspace(t)
	endpoint := newAppEndpoint(t, func(received callback) int {
		if received.Text == "poison" {
			return http.StatusInternalServerError
		}
		return http.StatusOK
	})
	workspace.installApp(t, endpoint, "UBOT", []string{"message.channels"}, []string{"channels:history"})
	workspace.repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	workspace.repository.SeedConversationMember("C1", "U1")
	workspace.repository.SeedConversationMember("C1", "UBOT")
	for _, text := range []string{"poison", "second", "third", "fourth", "fifth"} {
		if _, err := workspace.messages.Post(ctx, "T1", "U1", "C1", text, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	processor := workspace.processor(endpoint.server.Client())
	// The whole backlog drains in one cycle rather than one record per cycle.
	if _, err := processor.RunOnce(ctx); err == nil {
		t.Fatal("the failing callback was not reported")
	}
	delivered := map[string]bool{}
	poisonTries := 0
	for _, received := range endpoint.received() {
		if received.Text == "poison" {
			poisonTries++
			continue
		}
		delivered[received.Text] = true
	}
	for _, text := range []string{"second", "third", "fourth", "fifth"} {
		if !delivered[text] {
			t.Fatalf("%q was held back behind the failing callback: %+v", text, endpoint.received())
		}
	}
	// The failed record keeps its own Slack schedule — retried with the next
	// X-Slack-Retry-Num each time it falls due (at once, here, because the
	// processor's clock is in the past) until the retries run out — and
	// nothing else is re-sent.
	for cycle := 0; cycle < 4; cycle++ {
		_, _ = processor.RunOnce(ctx)
	}
	var retries []string
	for _, received := range endpoint.received() {
		if received.Text == "poison" {
			retries = append(retries, received.RetryNum+"/"+received.RetryReason)
		} else if received.RetryNum != "" {
			t.Fatalf("a delivered callback was retried: %+v", received)
		}
	}
	if strings.Join(retries, ",") != "/,1/http_error,2/http_error,3/http_error" {
		t.Fatalf("poison deliveries=%v, want the first try and Slack's three retries", retries)
	}
	if cursor, err := workspace.repository.GetAppEventCursor(ctx, workspace.appID(t), "http"); err != nil || cursor.Pending != 0 {
		t.Fatalf("cursor=%+v err=%v, want every record settled", cursor, err)
	}
}

func (w *deliveryWorkspace) appID(t *testing.T) domain.AppID {
	t.Helper()
	snapshots, err := w.repository.ListInstalledApps(context.Background())
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("installed apps=%d err=%v", len(snapshots), err)
	}
	return snapshots[0].App.ID
}

// A slow endpoint must not serialise another app's callbacks behind it.
func TestASlowAppDoesNotDelayAnotherApp(t *testing.T) {
	ctx := context.Background()
	workspace := newDeliveryWorkspace(t)
	fastReceived := make(chan struct{})
	var once sync.Once
	fast := newAppEndpoint(t, func(callback) int {
		once.Do(func() { close(fastReceived) })
		return http.StatusOK
	})
	slowSawFast := make(chan bool, 1)
	slow := newAppEndpoint(t, func(callback) int {
		// The slow app answers only once the fast app has been called, or
		// gives up well inside the three-second delivery timeout. Serial
		// delivery would reach the fast app only after this returns.
		select {
		case <-fastReceived:
			slowSawFast <- true
		case <-time.After(2 * time.Second):
			slowSawFast <- false
		}
		return http.StatusOK
	})
	workspace.installApp(t, slow, "USLOW", []string{"message.channels"}, []string{"channels:history"})
	workspace.installApp(t, fast, "UFAST", []string{"message.channels"}, []string{"channels:history"})
	workspace.repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	for _, member := range []domain.UserID{"U1", "USLOW", "UFAST"} {
		workspace.repository.SeedConversationMember("C1", member)
	}
	if _, err := workspace.messages.Post(ctx, "T1", "U1", "C1", "hello", "", ""); err != nil {
		t.Fatal(err)
	}
	// Both endpoints are httptest TLS servers sharing one test CA, so either
	// client trusts both.
	if _, err := workspace.processor(slow.server.Client()).RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !<-slowSawFast {
		t.Fatal("the fast app was not called while the slow app's callback was in flight")
	}
}

// failingProjectionStore fails the message read a projection needs a set
// number of times, which is a failure on this side of the delivery.
type failingProjectionStore struct {
	*memory.Store
	mutex    sync.Mutex
	failures int
}

func (s *failingProjectionStore) GetMessage(ctx context.Context, id domain.MessageID) (domain.Message, error) {
	s.mutex.Lock()
	if s.failures > 0 {
		s.failures--
		s.mutex.Unlock()
		return domain.Message{}, errors.New("message storage is unavailable")
	}
	s.mutex.Unlock()
	return s.Store.GetMessage(ctx, id)
}

// A failure to build the event on this side is not a delivery the app failed:
// it must not use up one of the app's retries, and its reason — not one of
// Slack's — must never reach the app.
func TestAnInternalFailureIsNeitherAnAttemptNorAReasonTheAppSees(t *testing.T) {
	ctx := context.Background()
	workspace := newDeliveryWorkspace(t)
	endpoint := newAppEndpoint(t, func(callback) int { return http.StatusOK })
	appID := workspace.installApp(t, endpoint, "UBOT", []string{"message.channels"}, []string{"channels:history"})
	workspace.repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	workspace.repository.SeedConversationMember("C1", "U1")
	workspace.repository.SeedConversationMember("C1", "UBOT")
	message, err := workspace.messages.Post(ctx, "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// The snapshot in the journal is what a projection normally reads; this
	// record is the legacy identifier-only shape that must read the message.
	legacy, err := events.New("evt_legacy", "T1", "U1", events.NewPayload("message.created",
		events.String("channel_id", "C1"), events.String("message_id", string(message.ID)),
	), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.repository.AppendEvent(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	flaky := &failingProjectionStore{Store: workspace.repository, failures: 3}
	processor := workspace.processor(endpoint.server.Client())
	processor.Store = flaky
	for cycle := 0; cycle < 5; cycle++ {
		_, _ = processor.RunOnce(ctx)
	}
	var legacyCallbacks []callback
	for _, received := range endpoint.received() {
		if received.EventID == "evt_legacy" {
			legacyCallbacks = append(legacyCallbacks, received)
		}
	}
	if len(legacyCallbacks) != 1 || legacyCallbacks[0].RetryNum != "" || legacyCallbacks[0].RetryReason != "" {
		t.Fatalf("callbacks after internal failures=%+v, want one first delivery with no retry headers", legacyCallbacks)
	}
	attempts, err := workspace.repository.ListAppDeliveryAttempts(ctx, appID, "http", 0)
	if err != nil {
		t.Fatal(err)
	}
	internal := 0
	for _, attempt := range attempts {
		if attempt.Reason == "event_projection_failed" {
			internal++
		}
	}
	if internal == 0 {
		t.Fatalf("the internal failures are not visible to operators: %+v", attempts)
	}
}

// A record that fans out into several callbacks is retried only for the
// callbacks the app did not accept. Re-posting the accepted ones delivered
// them twice.
func TestAFannedOutRecordRetriesOnlyTheCallbacksTheAppFailed(t *testing.T) {
	ctx := context.Background()
	workspace := newDeliveryWorkspace(t)
	var mentionFailures sync.Once
	endpoint := newAppEndpoint(t, func(received callback) int {
		status := http.StatusOK
		if received.Type == "app_mention" {
			mentionFailures.Do(func() { status = http.StatusServiceUnavailable })
		}
		return status
	})
	workspace.installApp(t, endpoint, "UBOT", []string{"message.channels", "app_mention"}, []string{"channels:history", "app_mentions:read"})
	workspace.repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	workspace.repository.SeedConversationMember("C1", "U1")
	workspace.repository.SeedConversationMember("C1", "UBOT")
	if _, err := workspace.messages.Post(ctx, "T1", "U1", "C1", "<@UBOT> deploy please", "", ""); err != nil {
		t.Fatal(err)
	}
	processor := workspace.processor(endpoint.server.Client())
	for cycle := 0; cycle < 3; cycle++ {
		_, _ = processor.RunOnce(ctx)
	}
	counts := map[string]int{}
	eventIDs := map[string]string{}
	for _, received := range endpoint.received() {
		counts[received.Type]++
		if previous, seen := eventIDs[received.Type]; seen && previous != received.EventID {
			t.Fatalf("%s changed event_id across its retry: %q then %q", received.Type, previous, received.EventID)
		}
		eventIDs[received.Type] = received.EventID
	}
	if counts["message"] != 1 || counts["app_mention"] != 2 {
		t.Fatalf("callbacks=%+v, want the message once and the app_mention on its first try and one retry", endpoint.received())
	}
	if eventIDs["message"] == eventIDs["app_mention"] {
		t.Fatalf("the message and its app_mention share event_id %q", eventIDs["message"])
	}
}
