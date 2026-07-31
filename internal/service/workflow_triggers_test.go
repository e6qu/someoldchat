package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func seedWorkflowTriggerWorld(t *testing.T) (context.Context, *memory.Store, Messages, domain.WorkflowDefinition) {
	t.Helper()
	ctx := context.Background()
	repository := memory.New()
	now := time.Now().UTC()
	for _, seed := range []error{
		repository.SeedWorkspace(domain.Workspace{ID: "T1", Name: "Test"}),
		repository.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "owner"}),
		repository.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "member"}),
		repository.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}),
		repository.SeedConversation(domain.Conversation{ID: "C2", WorkspaceID: "T1", Name: "other"}),
		repository.SeedConversationMember("C1", "U1"),
		repository.SeedConversationMember("C1", "U2"),
	} {
		if seed != nil {
			t.Fatal(seed)
		}
	}
	if err := repository.CreateApp(ctx, domain.App{
		ID: "A1", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Automation", ClientID: "client",
		SigningSecretHash: "signing", SigningSecretCiphertext: "cipher", VerificationTokenHash: "verify",
		VerificationTokenCiphertext: "cipher", ManifestVersion: 1, Distribution: "private",
		CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "A1", Version: 1, CreatedBy: "U1", CreatedAt: now,
		Manifest: `{
			"display_information":{"name":"Automation"},
			"settings":{"function_runtime":"remote"},
			"functions":{"triage":{
				"title":"Triage incident","description":"Classifies one incident",
				"input_parameters":{"properties":{},"required":[]},
				"output_parameters":{"properties":{},"required":[]}
			},"notify":{
				"title":"Notify channel","description":"Posts the result",
				"input_parameters":{"properties":{},"required":[]},
				"output_parameters":{"properties":{},"required":[]}
			}}
		}`,
	}, domain.OAuthClient{ID: "client", SecretHash: "secret", AppID: "A1"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: repository, AppCredentialKey: []byte("0123456789abcdef0123456789abcdef")}
	workflow, err := messages.CreateWorkflow(ctx, "T1", "U1", domain.WorkflowDefinition{
		AppID: "A1", Title: "Incident triage", InputSchema: `{}`, Steps: `[{"function_id":"triage","title":"Classify"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err = messages.UpdateWorkflow(ctx, "T1", "U1", workflow, workflow.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, repository, messages, workflow
}

func TestNextWorkflowScheduledRunStepsCalendarsInTheConfiguredZone(t *testing.T) {
	daily := `{"start_time":"2026-03-27T09:00:00+02:00","timezone":"Europe/Helsinki","frequency":{"type":"daily"}}`
	first, err := NextWorkflowScheduledRun(daily, time.Date(2026, time.March, 27, 0, 0, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.In(mustLocation(t, "Europe/Helsinki")); got.Hour() != 9 || got.Day() != 27 {
		t.Fatalf("first occurrence=%s, want 09:00 on the 27th in Europe/Helsinki", got)
	}
	// Helsinki springs forward on 2026-03-29: a wall-clock daily schedule stays
	// at 09:00 local even though the UTC offset changes.
	afterSpringForward, err := NextWorkflowScheduledRun(daily, time.Date(2026, time.March, 29, 0, 0, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := afterSpringForward.In(mustLocation(t, "Europe/Helsinki")); got.Hour() != 9 || got.Day() != 29 {
		t.Fatalf("spring-forward occurrence=%s, want 09:00 on the 29th in Europe/Helsinki", got)
	}
	weekly, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-05T10:30:00Z","timezone":"UTC","frequency":{"type":"weekly","interval":2}}`,
		time.Date(2026, time.January, 5, 10, 30, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.January, 19, 10, 30, 0, 0, time.UTC); !weekly.Equal(want) {
		t.Fatalf("biweekly occurrence=%s, want %s", weekly, want)
	}
	monthly, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-31T08:00:00Z","timezone":"UTC","frequency":{"type":"monthly"}}`,
		time.Date(2026, time.January, 31, 8, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	// A 31st-of-the-month schedule clamps to the last day of shorter months
	// instead of drifting into the following month.
	if want := time.Date(2026, time.February, 28, 8, 0, 0, 0, time.UTC); !monthly.Equal(want) {
		t.Fatalf("month-end occurrence=%s, want %s", monthly, want)
	}
	// The clamped occurrence does not drift the schedule: the fire after it
	// lands back on the 31st.
	afterClamp, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-31T08:00:00Z","timezone":"UTC","frequency":{"type":"monthly"}}`,
		monthly, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.March, 31, 8, 0, 0, 0, time.UTC); !afterClamp.Equal(want) {
		t.Fatalf("post-clamp occurrence=%s, want %s", afterClamp, want)
	}
	// An explicit day-of-month uses the same clamp from any start day.
	explicitDay, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-15T08:00:00Z","timezone":"UTC","frequency":{"type":"monthly","day":31}}`,
		time.Date(2026, time.January, 15, 8, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.February, 28, 8, 0, 0, 0, time.UTC); !explicitDay.Equal(want) {
		t.Fatalf("explicit month-end occurrence=%s, want %s", explicitDay, want)
	}
	// Named weekdays fire on their own days inside the interval's weeks.
	weekdays := `{"start_time":"2026-01-05T09:00:00Z","timezone":"UTC","frequency":{"type":"weekly","weekdays":["mon","wed"]}}`
	firstWeekday, err := NextWorkflowScheduledRun(weekdays, time.Date(2026, time.January, 5, 9, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.January, 7, 9, 0, 0, 0, time.UTC); !firstWeekday.Equal(want) {
		t.Fatalf("first weekday occurrence=%s, want %s", firstWeekday, want)
	}
	secondWeekday, err := NextWorkflowScheduledRun(weekdays, firstWeekday, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.January, 12, 9, 0, 0, 0, time.UTC); !secondWeekday.Equal(want) {
		t.Fatalf("second weekday occurrence=%s, want %s", secondWeekday, want)
	}
	// Interval weeks anchor on the start's week: weekdays in the skipped
	// weeks do not fire.
	biweekly, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-05T09:00:00Z","timezone":"UTC","frequency":{"type":"weekly","interval":2,"weekdays":["mon","wed"]}}`,
		time.Date(2026, time.January, 7, 9, 0, 0, 0, time.UTC), false)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.January, 19, 9, 0, 0, 0, time.UTC); !biweekly.Equal(want) {
		t.Fatalf("biweekly weekday occurrence=%s, want %s", biweekly, want)
	}
	hourly, err := NextWorkflowScheduledRun(
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"hourly","interval":6}}`,
		time.Date(2026, time.January, 2, 13, 0, 0, 0, time.UTC), true)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, time.January, 2, 18, 0, 0, 0, time.UTC); !hourly.Equal(want) {
		t.Fatalf("six-hour occurrence=%s, want %s", hourly, want)
	}
	for _, raw := range []string{
		`{"start_time":"not-a-time","timezone":"UTC","frequency":{"type":"daily"}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"Not/AZone","frequency":{"type":"daily"}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"yearly"}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"daily","interval":0}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"daily","interval":367}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"daily","weekdays":["mon"]}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"weekly","weekdays":["mon","mon"]}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"weekly","weekdays":["monday"]}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"weekly","day":15}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"monthly","day":32}}`,
		`{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"monthly","day":0}}`,
	} {
		if _, err := NextWorkflowScheduledRun(raw, time.Now(), true); !errors.Is(err, ErrInvalidTriggerConfig) {
			t.Fatalf("schedule %s error=%v, want ErrInvalidTriggerConfig", raw, err)
		}
	}
}

func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func TestScheduledWorkflowTriggerComputesAndClearsNextRun(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	schedule := `{"start_time":"2020-01-01T09:00:00Z","timezone":"UTC","frequency":{"type":"daily"}}`
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Daily", Type: "scheduled", Config: schedule, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if trigger.NextRunAt.IsZero() || !trigger.NextRunAt.After(time.Now().UTC().Add(-time.Minute)) {
		t.Fatalf("next run=%s, want a future occurrence", trigger.NextRunAt)
	}
	if got := trigger.NextRunAt.In(mustLocation(t, "UTC")); got.Hour() != 9 || got.Minute() != 0 {
		t.Fatalf("next run=%s, want 09:00 UTC", got)
	}
	disabled, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		ID: trigger.ID, WorkflowID: workflow.ID, Title: "Daily", Type: "scheduled", Config: schedule, Enabled: false,
	}, trigger.Version)
	if err != nil {
		t.Fatal(err)
	}
	if !disabled.NextRunAt.IsZero() {
		t.Fatalf("disabled trigger kept next run %s", disabled.NextRunAt)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Broken", Type: "scheduled", Config: `{"start_time":"soon"}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("invalid schedule error=%v, want ErrInvalidTriggerConfig", err)
	}
}

func TestWebhookWorkflowTriggerSecretLifecycle(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Hook", Type: "webhook", Config: `{}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trigger.Config, "webhook_secret_hash") || strings.Contains(trigger.Config, `"secret"`) {
		t.Fatalf("webhook config=%s, want stored hash and ciphertext only", trigger.Config)
	}
	invokeURL, err := messages.WebhookTriggerURL(ctx, "T1", "U1", trigger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(invokeURL, "/services/triggers/T1/"+string(trigger.ID)+"/") {
		t.Fatalf("webhook URL=%s", invokeURL)
	}
	if _, err := messages.WebhookTriggerURL(ctx, "T1", "U2", trigger.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("non-owner webhook URL error=%v, want ErrNotFound", err)
	}
	// Enable/disable and rename edits preserve the URL: rotation is a deliberate
	// act, not a side effect of every update.
	updated, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		ID: trigger.ID, WorkflowID: workflow.ID, Title: "Renamed hook", Type: "webhook", Config: `{}`, Enabled: false,
	}, trigger.Version)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Config != trigger.Config {
		t.Fatal("enable/disable rotated the webhook secret")
	}
	secret := invokeURL[strings.LastIndex(invokeURL, "/")+1:]
	if _, err := messages.RunWebhookTrigger(ctx, "T1", trigger.ID, "wrong-secret", `{}`); !errors.Is(err, ErrWebhookTriggerSecret) {
		t.Fatalf("wrong secret error=%v, want ErrWebhookTriggerSecret", err)
	}
	if _, err := messages.RunWebhookTrigger(ctx, "T1", trigger.ID, secret, `{}`); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("disabled trigger error=%v, want ErrConflict", err)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		ID: trigger.ID, WorkflowID: workflow.ID, Title: "Renamed hook", Type: "webhook", Config: updated.Config, Enabled: true,
	}, updated.Version); err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunWebhookTrigger(ctx, "T1", trigger.ID, secret, `{"source":"hook"}`)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != domain.WorkflowRunRunning || run.ActorID != "U1" || run.Inputs != `{"source":"hook"}` {
		t.Fatalf("webhook run=%+v", run)
	}
	if _, err := messages.RunWebhookTrigger(ctx, "T1", "Ft-missing", secret, `{}`); !errors.Is(err, ErrWebhookTriggerSecret) {
		t.Fatalf("unknown trigger error=%v, want ErrWebhookTriggerSecret", err)
	}
}

func TestEventWorkflowTriggerValidation(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Missing channel", Type: "message", Config: `{}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("channel-less message trigger error=%v, want ErrInvalidTriggerConfig", err)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Unknown channel", Type: "message", Config: `{"channel_ids":["C-missing"]}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("unknown channel error=%v, want ErrInvalidTriggerConfig", err)
	}
	reaction, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Eyes", Type: "reaction", Config: `{"channel_ids":["C1"],"reaction":":eyes:"}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reaction.Config, `"reaction":"eyes"`) {
		t.Fatalf("reaction config=%s, want colons stripped", reaction.Config)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Unknown list", Type: "list", Config: `{"list_id":"L-missing","event":"created"}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("unknown list error=%v, want ErrInvalidTriggerConfig", err)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Bad event", Type: "list", Config: `{"list_id":"L1","event":"deleted"}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("unknown list event error=%v, want ErrInvalidTriggerConfig", err)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Typed", Type: "carousel", Config: `{}`, Enabled: true,
	}, 0); !errors.Is(err, ErrInvalidTriggerConfig) {
		t.Fatalf("unknown type error=%v, want ErrInvalidTriggerConfig", err)
	}
}

func TestAutomaticWorkflowRunBypassesLinkPermissionButNotPublication(t *testing.T) {
	ctx, _, messages, workflow := seedWorkflowTriggerWorld(t)
	trigger, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "On message", Type: "message", Config: `{"channel_ids":["C1"]}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Even with the link permission narrowed to somebody else, an automatic fire
	// runs: the permission gates who may click, not the configured condition.
	if _, err := messages.SetTriggerPermission(ctx, "T1", "U1", "A1", trigger.ID, domain.AutomationPermission{
		PermissionType: "named_entities", UserIDs: []domain.UserID{"U2"},
	}); err != nil {
		t.Fatal(err)
	}
	run, err := messages.RunAutomaticWorkflow(ctx, "T1", trigger.ID, "C1", `{}`, "auto-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.ActorID != "U1" || run.ConversationID != "C1" {
		t.Fatalf("automatic run=%+v", run)
	}
	replay, err := messages.RunAutomaticWorkflow(ctx, "T1", trigger.ID, "C1", `{}`, "auto-1")
	if err != nil || replay.ID != run.ID {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
	disabled, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		ID: trigger.ID, WorkflowID: workflow.ID, Title: "On message", Type: "message", Config: `{"channel_ids":["C1"]}`, Enabled: false,
	}, trigger.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.RunAutomaticWorkflow(ctx, "T1", disabled.ID, "C1", `{}`, "auto-2"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("disabled trigger error=%v, want ErrConflict", err)
	}
}

func TestDispatchWorkflowEventTriggersMatchesAndAdvances(t *testing.T) {
	ctx, repository, messages, workflow := seedWorkflowTriggerWorld(t)
	keyword, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Incidents", Type: "message",
		Config: `{"channel_ids":["C1"],"keyword":"incident"}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	reaction, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Eyes", Type: "reaction",
		Config: `{"channel_ids":["C1"],"reaction":"eyes"}`, Enabled: true,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := messages.SetWorkflowTrigger(ctx, "T1", "U1", domain.WorkflowTrigger{
		WorkflowID: workflow.ID, Title: "Welcome", Type: "join", Config: `{"channel_ids":["C2"]}`, Enabled: true,
	}, 0); err != nil {
		t.Fatal(err)
	}

	post := func(text string) domain.Message {
		t.Helper()
		message, err := messages.PostMessageAs(ctx, "T1", "U2", domain.MessagePostRequest{Conversation: "C1", Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return message
	}
	post("a routine update")
	incident := post("an incident needs triage")

	started, err := messages.DispatchWorkflowEventTriggers(ctx, "T1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if started != 1 {
		t.Fatalf("started=%d, want exactly the keyword-matching fire", started)
	}
	runs, _, _, err := repository.ListWorkflowRuns(ctx, "T1", workflow.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	runsPage := runs[0]
	if runsPage.TriggerID != keyword.ID || runsPage.ConversationID != "C1" || runsPage.ActorID != "U1" {
		t.Fatalf("keyword run=%+v", runsPage)
	}

	incidentTS := domain.NewMessageTimestamp(incident.CreatedAt)
	if err := messages.AddReaction(ctx, "T1", "U2", "C1", incidentTS, "eyes"); err != nil {
		t.Fatal(err)
	}
	if err := messages.AddReaction(ctx, "T1", "U2", "C1", incidentTS, "thumbsup"); err != nil {
		t.Fatal(err)
	}
	started, err = messages.DispatchWorkflowEventTriggers(ctx, "T1", 100)
	if err != nil || started != 1 {
		t.Fatalf("reaction dispatch started=%d err=%v, want exactly the :eyes: fire", started, err)
	}
	runs, _, _, err = repository.ListWorkflowRuns(ctx, "T1", workflow.ID, domain.PageRequest{Limit: 10})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after reaction=%d err=%v", len(runs), err)
	}
	reactionRuns := 0
	for _, candidate := range runs {
		if candidate.TriggerID == reaction.ID {
			reactionRuns++
		}
	}
	if reactionRuns != 1 {
		t.Fatalf("reaction runs=%d, want exactly one for trigger %s", reactionRuns, reaction.ID)
	}

	// The cursor is durable state: a second sweep over the same events starts
	// nothing, and the join trigger still fires when its channel sees a member.
	started, err = messages.DispatchWorkflowEventTriggers(ctx, "T1", 100)
	if err != nil || started != 0 {
		t.Fatalf("replay dispatch started=%d err=%v, want 0", started, err)
	}
	if _, err := messages.JoinConversation(ctx, "T1", "U2", "C2"); err != nil {
		t.Fatal(err)
	}
	started, err = messages.DispatchWorkflowEventTriggers(ctx, "T1", 100)
	if err != nil || started != 1 {
		t.Fatalf("join dispatch started=%d err=%v, want 1", started, err)
	}
}
