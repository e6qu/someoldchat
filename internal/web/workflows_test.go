package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func seedWorkflowApp(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	_, mux, csrf := seedWorkflowAppWithStore(t)
	return mux, csrf
}

func seedWorkflowAppWithStore(t *testing.T) (*memory.Store, *http.ServeMux, string) {
	t.Helper()
	repository, mux := browserWorkspace(t, auth.AllScopes())
	for _, user := range []domain.User{{ID: "U2", WorkspaceID: "T1", Name: "member two"}, {ID: "U3", WorkspaceID: "T1", Name: "member three"}} {
		if err := repository.SeedUser(user); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	manifest := `{"display_information":{"name":"Workflow tools"},"settings":{"function_runtime":"remote"},"functions":{"triage":{"title":"Triage request","description":"Triage one request","input_parameters":{"properties":{"item":{"type":"string","title":"Item"}},"required":["item"]},"output_parameters":{"properties":{"result":{"type":"string","title":"Result"}},"required":["result"]}},"notify":{"title":"Notify channel","description":"Post a notification","input_parameters":{"properties":{}},"output_parameters":{"properties":{}}}}}`
	if err := repository.CreateApp(context.Background(), domain.App{
		ID: "Aworkflow", DevelopmentWorkspaceID: "T1", OwnerID: "U1", Name: "Workflow tools", ClientID: "workflow-client",
		SigningSecretHash: "signing-hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "verification-hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}, domain.AppManifestRevision{
		AppID: "Aworkflow", Version: 1, Manifest: manifest, CreatedBy: "U1", CreatedAt: now,
	}, domain.OAuthClient{ID: "workflow-client", SecretHash: "client-hash", AppID: "Aworkflow"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateAppInstallation(context.Background(), domain.AppInstallation{
		AppID: "Aworkflow", WorkspaceID: "T1", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return repository, mux, auth.CSRFToken("session")
}

func TestWorkflowPagesAgreeWithTheContentSecurityPolicy(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	for _, target := range []string{"/app/workflows", workflowURL} {
		response := get(t, mux, target)
		policy := response.Header().Get("Content-Security-Policy")
		if policy == "" {
			t.Fatalf("%s carries no content security policy", target)
		}
		bodies := inlineScriptBodies(response.Body.String())
		if len(bodies) == 0 {
			t.Fatalf("%s renders no inline script", target)
		}
		for _, body := range bodies {
			digest := sha256.Sum256([]byte(body))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			if !strings.Contains(policy, hash) {
				t.Fatalf("%s serves an inline script the policy blocks: %s", target, hash)
			}
		}
	}
}

func TestWorkflowBuilderPublishesTriggersAndStartsADurableRun(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	directory := get(t, mux, "/app/workflows")
	if directory.Code != http.StatusOK {
		t.Fatalf("directory=%d: %s", directory.Code, directory.Body)
	}
	requireContains(t, "workflow directory", directory.Body.String(), "Create a workflow", "Workflow tools", "Triage request")

	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "description": {"Classify and notify"},
		"app_id": {"Aworkflow"}, "function_callback": {"triage"}, "callback_id": {"incident-triage"},
	}.Encode(), false)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create=%d: %s", created.Code, created.Body)
	}
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	if !regexp.MustCompile(`^/app/workflows/Wf[0-9a-f]+$`).MatchString(workflowURL) {
		t.Fatalf("workflow location=%q", created.Header().Get("Location"))
	}
	page := get(t, mux, workflowURL)
	requireContains(t, "workflow draft", page.Body.String(), "Incident triage", "draft", "Triage request", `option value="triage" selected`)

	published := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "description": {"Classify and notify"},
		"callback_id": {"incident-triage"}, "input_schema": {`{}`}, "step_1": {"triage"}, "step_2": {"notify"},
		"action": {"publish"},
	}.Encode(), false)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish=%d: %s", published.Code, published.Body)
	}
	page = get(t, mux, published.Header().Get("Location"))
	requireContains(t, "published workflow", page.Body.String(), "published", "published version 2", "Unpublish")

	triggered := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Start incident triage"}, "type": {"link"},
	}.Encode(), false)
	if triggered.Code != http.StatusSeeOther {
		t.Fatalf("create trigger=%d: %s", triggered.Code, triggered.Body)
	}
	page = get(t, mux, triggered.Header().Get("Location"))
	requireContains(t, "workflow trigger", page.Body.String(), "Start incident triage", "link · enabled", "Run", "Disable")
	trigger := regexp.MustCompile(`/app/workflows/Wf[0-9a-f]+/triggers/(Ft[0-9a-f]+)/run`).FindStringSubmatch(page.Body.String())
	key := regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(trigger) != 2 || len(key) != 2 {
		t.Fatalf("run form missing: %s", page.Body)
	}

	started := postForm(t, mux, workflowURL+"/triggers/"+trigger[1]+"/run", url.Values{
		"_csrf": {csrf}, "idempotency_key": {key[1]},
	}.Encode(), false)
	if started.Code != http.StatusSeeOther {
		t.Fatalf("run=%d: %s", started.Code, started.Body)
	}
	runURL := started.Header().Get("Location")
	if !regexp.MustCompile(`^/app/workflows/runs/Wx[0-9a-f]+$`).MatchString(runURL) {
		t.Fatalf("run location=%q", runURL)
	}
	runPage := get(t, mux, runURL)
	if runPage.Code != http.StatusOK {
		t.Fatalf("run page=%d: %s", runPage.Code, runPage.Body)
	}
	requireContains(t, "durable workflow run", runPage.Body.String(), "Workflow run", "running", "An app function is running")

	unpublished := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"2"}, "title": {"Incident triage"}, "description": {"Classify and notify"},
		"callback_id": {"incident-triage"}, "input_schema": {`{}`}, "step_1": {"triage"}, "step_2": {"notify"},
		"action": {"unpublish"},
	}.Encode(), false)
	if unpublished.Code != http.StatusSeeOther {
		t.Fatalf("unpublish=%d: %s", unpublished.Code, unpublished.Body)
	}
	page = get(t, mux, unpublished.Header().Get("Location"))
	requireContains(t, "unpublished workflow", page.Body.String(), "Workflow unpublished", "disabled", "published version 2")
	if strings.Contains(page.Body.String(), `<button type="submit">Run</button>`) {
		t.Fatalf("unpublished workflow still exposes a run action: %s", page.Body)
	}
}

func TestWorkflowBuilderConfiguresScheduledWebhookAndEventTriggers(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	published := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish=%d: %s", published.Code, published.Body)
	}

	scheduled := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Every morning"}, "type": {"scheduled"},
		"schedule_start": {"2030-05-04T09:30"}, "schedule_timezone": {"Europe/Helsinki"},
		"schedule_frequency": {"daily"}, "schedule_interval": {"1"},
	}.Encode(), false)
	if scheduled.Code != http.StatusSeeOther {
		t.Fatalf("scheduled trigger=%d: %s", scheduled.Code, scheduled.Body)
	}
	page := get(t, mux, scheduled.Header().Get("Location"))
	requireContains(t, "scheduled trigger", page.Body.String(), "Every morning", "Every 1 daily · Europe/Helsinki", "Next run")

	weekdays := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Weekday sync"}, "type": {"scheduled"},
		"schedule_start": {"2030-05-06T09:00"}, "schedule_timezone": {"UTC"},
		"schedule_frequency": {"weekly"}, "schedule_interval": {"1"},
		"schedule_weekday_mon": {"1"}, "schedule_weekday_wed": {"1"},
	}.Encode(), false)
	if weekdays.Code != http.StatusSeeOther {
		t.Fatalf("weekday trigger=%d: %s", weekdays.Code, weekdays.Body)
	}
	page = get(t, mux, weekdays.Header().Get("Location"))
	requireContains(t, "weekday trigger", page.Body.String(), "Weekday sync", "Every week on mon, wed · UTC")

	monthEnd := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Month end"}, "type": {"scheduled"},
		"schedule_start": {"2030-01-15T09:00"}, "schedule_timezone": {"UTC"},
		"schedule_frequency": {"monthly"}, "schedule_day": {"31"},
	}.Encode(), false)
	if monthEnd.Code != http.StatusSeeOther {
		t.Fatalf("month-end trigger=%d: %s", monthEnd.Code, monthEnd.Body)
	}
	page = get(t, mux, monthEnd.Header().Get("Location"))
	requireContains(t, "month-end trigger", page.Body.String(), "Month end", "Every month on day 31 · UTC")

	invalidDay := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Broken day"}, "type": {"scheduled"},
		"schedule_start": {"2030-01-15T09:00"}, "schedule_timezone": {"UTC"},
		"schedule_frequency": {"monthly"}, "schedule_day": {"32"},
	}.Encode(), false)
	if invalidDay.Code != http.StatusBadRequest {
		t.Fatalf("invalid day=%d: %s", invalidDay.Code, invalidDay.Body)
	}

	broken := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Broken"}, "type": {"scheduled"},
		"schedule_start": {"2030-05-04T09:30"}, "schedule_timezone": {"Not/AZone"},
		"schedule_frequency": {"daily"},
	}.Encode(), false)
	if broken.Code != http.StatusBadRequest {
		t.Fatalf("invalid timezone=%d: %s", broken.Code, broken.Body)
	}

	hooked := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Deploy hook"}, "type": {"webhook"},
	}.Encode(), false)
	if hooked.Code != http.StatusSeeOther {
		t.Fatalf("webhook trigger=%d: %s", hooked.Code, hooked.Body)
	}
	page = get(t, mux, hooked.Header().Get("Location"))
	requireContains(t, "webhook trigger", page.Body.String(), "Deploy hook", "Webhook URL", "/services/triggers/T1/")
	invokePath := regexp.MustCompile(`(/services/triggers/T1/Ft[0-9a-f]+/[0-9a-f]+)`).FindString(page.Body.String())
	if invokePath == "" {
		t.Fatalf("webhook URL not rendered: %s", page.Body)
	}

	watched := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Watch incidents"}, "type": {"message"},
		"event_channel": {"Cdev"}, "event_keyword": {"incident"},
	}.Encode(), false)
	if watched.Code != http.StatusSeeOther {
		t.Fatalf("message trigger=%d: %s", watched.Code, watched.Body)
	}
	page = get(t, mux, watched.Header().Get("Location"))
	requireContains(t, "message trigger", page.Body.String(), "Watch incidents", "New messages in #general containing &#34;incident&#34;")

	// An enable/disable roundtrip carries the stored configuration: the
	// schedule and webhook URL survive the toggle instead of being wiped.
	message := ""
	for _, article := range strings.Split(page.Body.String(), `<article class="trigger">`) {
		if strings.Contains(article, "Watch incidents") {
			message = article
		}
	}
	if message == "" {
		t.Fatalf("message trigger article missing: %s", page.Body)
	}
	version := regexp.MustCompile(`name="version" value="(\d+)"`).FindStringSubmatch(message)
	config := regexp.MustCompile(`name="config" value="([^"]+)"`).FindStringSubmatch(message)
	triggerID := regexp.MustCompile(`triggers/(Ft[0-9a-f]+)"`).FindStringSubmatch(message)
	if len(version) != 2 || len(config) != 2 || len(triggerID) != 2 {
		t.Fatalf("message trigger form incomplete: %s", message)
	}
	toggled := postForm(t, mux, workflowURL+"/triggers/"+triggerID[1], url.Values{
		"_csrf": {csrf}, "version": {version[1]}, "title": {"Watch incidents"}, "type": {"message"},
		"config": {strings.ReplaceAll(config[1], "&#34;", `"`)}, "enabled": {"false"},
	}.Encode(), false)
	if toggled.Code != http.StatusSeeOther {
		t.Fatalf("toggle=%d: %s", toggled.Code, toggled.Body)
	}
	page = get(t, mux, toggled.Header().Get("Location"))
	requireContains(t, "disabled message trigger", page.Body.String(), "Watch incidents", "message · disabled", "containing &#34;incident&#34;")
}

func TestWorkflowBuilderStagesEditsOverAPublishedRevision(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)

	staged := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"2"}, "title": {"Incident triage v2"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"save"},
	}.Encode(), false)
	if staged.Code != http.StatusSeeOther {
		t.Fatalf("staged save=%d: %s", staged.Code, staged.Body)
	}
	page := get(t, mux, staged.Header().Get("Location"))
	requireContains(t, "staged workflow", page.Body.String(), "Staged changes saved", "Incident triage v2", "your staged changes are not yet published", "published version 2")

	published := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"3"}, "title": {"Incident triage v2"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish staged=%d: %s", published.Code, published.Body)
	}
	page = get(t, mux, published.Header().Get("Location"))
	requireContains(t, "republished workflow", page.Body.String(), "Workflow published", "published version 4")
	if strings.Contains(page.Body.String(), "your staged changes are not yet published") {
		t.Fatalf("published workflow still reports staged changes: %s", page.Body)
	}
}

func TestWorkflowBuilderDiscardsStagedChanges(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)

	staged := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"2"}, "title": {"Incident triage v2"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"save"},
	}.Encode(), false)
	if staged.Code != http.StatusSeeOther {
		t.Fatalf("staged save=%d: %s", staged.Code, staged.Body)
	}
	page := get(t, mux, staged.Header().Get("Location"))
	requireContains(t, "staged workflow", page.Body.String(), "Incident triage v2", "Discard changes")

	discarded := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"3"}, "title": {"Incident triage v2"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"discard"},
	}.Encode(), false)
	if discarded.Code != http.StatusSeeOther {
		t.Fatalf("discard=%d: %s", discarded.Code, discarded.Body)
	}
	page = get(t, mux, discarded.Header().Get("Location"))
	requireContains(t, "discarded workflow", page.Body.String(), "Staged changes discarded", "Incident triage", "published version 2")
	if strings.Contains(page.Body.String(), "Discard changes") || strings.Contains(page.Body.String(), "your staged changes are not yet published") {
		t.Fatalf("discarded workflow still reports staged changes: %s", page.Body)
	}

	staleDiscard := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"3"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"discard"},
	}.Encode(), false)
	if staleDiscard.Code != http.StatusConflict {
		t.Fatalf("stale discard=%d: %s", staleDiscard.Code, staleDiscard.Body)
	}
	requireContains(t, "stale discard", staleDiscard.Body.String(), "The workflow was not saved", "It changed elsewhere")
}

func TestWorkflowBuilderMarksPerStepChangesAgainstThePublishedRevision(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "step_2": {"notify"}, "action": {"publish"},
	}.Encode(), false)

	replaced := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"2"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "step_2": {"triage"}, "action": {"save"},
	}.Encode(), false)
	if replaced.Code != http.StatusSeeOther {
		t.Fatalf("staged save=%d: %s", replaced.Code, replaced.Body)
	}
	page := get(t, mux, replaced.Header().Get("Location"))
	requireContains(t, "changed step", page.Body.String(), `data-step-change="2"`, "changed")

	truncated := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"3"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"save"},
	}.Encode(), false)
	if truncated.Code != http.StatusSeeOther {
		t.Fatalf("staged truncate=%d: %s", truncated.Code, truncated.Body)
	}
	page = get(t, mux, truncated.Header().Get("Location"))
	requireContains(t, "removed step", page.Body.String(), "Removed from the published version", "Notify channel · notify", `data-removed-step="2"`)

	restored := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"4"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "step_2": {"triage"}, "step_3": {"notify"}, "action": {"save"},
	}.Encode(), false)
	if restored.Code != http.StatusSeeOther {
		t.Fatalf("staged restore=%d: %s", restored.Code, restored.Body)
	}
	page = get(t, mux, restored.Header().Get("Location"))
	requireContains(t, "added step", page.Body.String(), `data-step-change="2"`, "changed", `data-step-change="3"`, "added")
	if strings.Contains(page.Body.String(), `data-removed-step`) {
		t.Fatalf("restored workflow still lists removed steps: %s", page.Body)
	}
}

func TestWorkflowBuilderShowsRunActivityToTheOwner(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	page := get(t, mux, workflowURL)
	requireContains(t, "empty activity", page.Body.String(), "Run activity", "No runs yet")
	postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)
	triggered := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Start incident triage"}, "type": {"link"},
	}.Encode(), false)
	page = get(t, mux, triggered.Header().Get("Location"))
	trigger := regexp.MustCompile(`/app/workflows/Wf[0-9a-f]+/triggers/(Ft[0-9a-f]+)/run`).FindStringSubmatch(page.Body.String())
	key := regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(trigger) != 2 || len(key) != 2 {
		t.Fatalf("run form missing: %s", page.Body)
	}
	started := postForm(t, mux, workflowURL+"/triggers/"+trigger[1]+"/run", url.Values{
		"_csrf": {csrf}, "idempotency_key": {key[1]},
	}.Encode(), false)
	if started.Code != http.StatusSeeOther {
		t.Fatalf("run=%d: %s", started.Code, started.Body)
	}
	page = get(t, mux, workflowURL)
	requireContains(t, "running activity", page.Body.String(), "Run activity", "<b>1</b> running", "Start incident triage", `data-activity-run`)
	runLink := regexp.MustCompile(`href="(/app/workflows/runs/Wx[0-9a-f]+)"`).FindStringSubmatch(page.Body.String())
	if len(runLink) != 2 {
		t.Fatalf("activity run link missing: %s", page.Body)
	}

	unpublished := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"2"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"unpublish"},
	}.Encode(), false)
	if unpublished.Code != http.StatusSeeOther {
		t.Fatalf("unpublish=%d: %s", unpublished.Code, unpublished.Body)
	}
	page = get(t, mux, workflowURL)
	requireContains(t, "cancelled activity", page.Body.String(), "<b>0</b> running", "<b>1</b> cancelled")
}

func TestWorkflowBuilderRunsFormAndButtonSteps(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Interactive"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	published := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Interactive"}, "input_schema": {`{}`},
		"step_type_1": {"form"}, "form_1": {`{"title":"Intake","inputs":{"name":"Name"}}`},
		"step_type_2": {"button"}, "button_label_2": {"Approve"},
		"step_type_3": {"function"}, "step_3": {"notify"},
		"action": {"publish"},
	}.Encode(), false)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish=%d: %s", published.Code, published.Body)
	}
	triggered := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Run"}, "type": {"link"},
	}.Encode(), false)
	page := get(t, mux, triggered.Header().Get("Location"))
	trigger := regexp.MustCompile(`/app/workflows/Wf[0-9a-f]+/triggers/(Ft[0-9a-f]+)/run`).FindStringSubmatch(page.Body.String())
	key := regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	if len(trigger) != 2 || len(key) != 2 {
		t.Fatalf("run form missing: %s", page.Body)
	}
	started := postForm(t, mux, workflowURL+"/triggers/"+trigger[1]+"/run", url.Values{
		"_csrf": {csrf}, "idempotency_key": {key[1]},
	}.Encode(), false)
	runURL := started.Header().Get("Location")
	runPage := get(t, mux, runURL)
	requireContains(t, "form step", runPage.Body.String(), "Intake", `name="field_name"`, `name="step_id"`, "Submit")
	stepID := regexp.MustCompile(`name="step_id" value="([^"]+)"`).FindStringSubmatch(runPage.Body.String())
	if len(stepID) != 2 {
		t.Fatalf("step id missing: %s", runPage.Body)
	}

	submitted := postForm(t, mux, "/app/workflows/runs/submit/"+strings.TrimPrefix(runURL, "/app/workflows/runs/"), url.Values{
		"_csrf": {csrf}, "step_id": {stepID[1]}, "field_name": {"Ada"},
	}.Encode(), false)
	if submitted.Code != http.StatusSeeOther {
		t.Fatalf("submit=%d: %s", submitted.Code, submitted.Body)
	}
	runPage = get(t, mux, runURL)
	requireContains(t, "button step", runPage.Body.String(), "Approve", "Confirm")
	buttonStep := regexp.MustCompile(`name="step_id" value="([^"]+)"`).FindStringSubmatch(runPage.Body.String())
	clicked := postForm(t, mux, "/app/workflows/runs/click/"+strings.TrimPrefix(runURL, "/app/workflows/runs/"), url.Values{
		"_csrf": {csrf}, "step_id": {buttonStep[1]},
	}.Encode(), false)
	if clicked.Code != http.StatusSeeOther {
		t.Fatalf("click=%d: %s", clicked.Code, clicked.Body)
	}
	runPage = get(t, mux, runURL)
	requireContains(t, "advanced run", runPage.Body.String(), "running", "An app function is running")
}

func TestWorkflowBuilderSavesAndShowsAnIcon(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"},
		"function_callback": {"triage"}, "icon": {"🚨"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	directory := get(t, mux, "/app/workflows")
	requireContains(t, "directory icon", directory.Body.String(), `class="wf-icon"`, "🚨")
	page := get(t, mux, workflowURL)
	requireContains(t, "builder icon", page.Body.String(), `name="icon" maxlength="64" value="🚨"`, `class="wf-icon"`)

	updated := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"icon": {"📋"}, "step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)
	if updated.Code != http.StatusSeeOther {
		t.Fatalf("update=%d: %s", updated.Code, updated.Body)
	}
	page = get(t, mux, workflowURL)
	requireContains(t, "updated icon", page.Body.String(), `value="📋"`)
}

func TestWorkflowOwnerAssignsManagersFromTheBuilder(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Managed workflow"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	page := get(t, mux, workflowURL)
	requireContains(t, "managers section", page.Body.String(), "Workflow managers", `name="manager_ids"`)

	saved := postForm(t, mux, workflowURL+"/managers", url.Values{
		"_csrf": {csrf}, "manager_ids": {"U2, U3"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("save managers=%d: %s", saved.Code, saved.Body)
	}
	page = get(t, mux, saved.Header().Get("Location"))
	requireContains(t, "saved managers", page.Body.String(), "Managers updated", `value="U2, U3"`)

	// A member ID that is not a workspace member is rejected.
	bad := postForm(t, mux, workflowURL+"/managers", url.Values{
		"_csrf": {csrf}, "manager_ids": {"Unobody"},
	}.Encode(), false)
	if bad.Code != http.StatusBadRequest && bad.Code != http.StatusNotFound {
		t.Fatalf("invalid manager=%d: %s", bad.Code, bad.Body)
	}
}

func TestWorkflowBuilderExportsRunsAndFormResponsesAsCSV(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Interactive"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	workflowID := strings.TrimPrefix(workflowURL, "/app/workflows/")
	postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Interactive"}, "input_schema": {`{}`},
		"step_type_1": {"form"}, "form_1": {`{"title":"Intake","inputs":{"name":"Name"}}`},
		"step_type_2": {"function"}, "step_2": {"notify"}, "action": {"publish"},
	}.Encode(), false)
	triggered := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Run"}, "type": {"link"},
	}.Encode(), false)
	page := get(t, mux, triggered.Header().Get("Location"))
	trigger := regexp.MustCompile(`/app/workflows/Wf[0-9a-f]+/triggers/(Ft[0-9a-f]+)/run`).FindStringSubmatch(page.Body.String())
	key := regexp.MustCompile(`name="idempotency_key" value="([^"]+)"`).FindStringSubmatch(page.Body.String())
	started := postForm(t, mux, workflowURL+"/triggers/"+trigger[1]+"/run", url.Values{
		"_csrf": {csrf}, "idempotency_key": {key[1]},
	}.Encode(), false)
	runURL := started.Header().Get("Location")
	runPage := get(t, mux, runURL)
	stepID := regexp.MustCompile(`name="step_id" value="([^"]+)"`).FindStringSubmatch(runPage.Body.String())
	postForm(t, mux, "/app/workflows/runs/submit/"+strings.TrimPrefix(runURL, "/app/workflows/runs/"), url.Values{
		"_csrf": {csrf}, "step_id": {stepID[1]}, "field_name": {"Ada"},
	}.Encode(), false)

	runs := get(t, mux, "/app/workflows/export/runs/"+workflowID)
	if runs.Code != http.StatusOK || !strings.Contains(runs.Header().Get("Content-Type"), "text/csv") {
		t.Fatalf("runs export=%d ct=%s", runs.Code, runs.Header().Get("Content-Type"))
	}
	requireContains(t, "runs csv", runs.Body.String(), "run_id,trigger,status,started_at,completed_at,error", "Run", "running")
	forms := get(t, mux, "/app/workflows/export/form-responses/"+workflowID)
	requireContains(t, "form csv", forms.Body.String(), "run_id,workflow_version,form,field,value,submitted_at", "Intake", "name", "Ada")

	if missing := get(t, mux, "/app/workflows/export/runs/Wfmissing"); missing.Code != http.StatusNotFound {
		t.Fatalf("missing workflow export=%d", missing.Code)
	}
}

func TestWorkflowBuilderCopiesAndDeletesAWorkflow(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
		"callback_id": {"incident-triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]

	copied := postForm(t, mux, workflowURL+"/copy", url.Values{"_csrf": {csrf}}.Encode(), false)
	if copied.Code != http.StatusSeeOther {
		t.Fatalf("copy=%d: %s", copied.Code, copied.Body)
	}
	copyURL := strings.Split(copied.Header().Get("Location"), "?")[0]
	if copyURL == workflowURL {
		t.Fatalf("copy redirected to the source workflow: %s", copied.Header().Get("Location"))
	}
	page := get(t, mux, copied.Header().Get("Location"))
	requireContains(t, "copied workflow", page.Body.String(), "Workflow copied", "Incident triage (copy)", "draft")

	version := regexp.MustCompile(`name="version" value="(\d+)"`).FindStringSubmatch(page.Body.String())
	if len(version) != 2 {
		t.Fatalf("copy page missing delete version: %s", page.Body)
	}
	stale := postForm(t, mux, copyURL+"/delete", url.Values{
		"_csrf": {csrf}, "version": {"99"},
	}.Encode(), false)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale delete=%d: %s", stale.Code, stale.Body)
	}
	missing := postForm(t, mux, "/app/workflows/Wfmissing/delete", url.Values{
		"_csrf": {csrf}, "version": {"1"},
	}.Encode(), false)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing workflow delete=%d: %s", missing.Code, missing.Body)
	}

	deleted := postForm(t, mux, copyURL+"/delete", url.Values{
		"_csrf": {csrf}, "version": {version[1]},
	}.Encode(), false)
	if deleted.Code != http.StatusSeeOther {
		t.Fatalf("delete=%d: %s", deleted.Code, deleted.Body)
	}
	directory := get(t, mux, deleted.Header().Get("Location"))
	requireContains(t, "directory after delete", directory.Body.String(), "Workflow deleted", "Incident triage")
	if strings.Contains(directory.Body.String(), "Incident triage (copy)") {
		t.Fatalf("deleted copy still listed: %s", directory.Body)
	}
	if gone := get(t, mux, copyURL); gone.Code != http.StatusNotFound {
		t.Fatalf("deleted workflow page=%d", gone.Code)
	}
}

func TestWorkflowBuilderRejectsAFunctionFromAnotherApp(t *testing.T) {
	mux, csrf := seedWorkflowApp(t)
	response := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Invalid workflow"}, "app_id": {"Aworkflow"}, "function_callback": {"missing"},
	}.Encode(), false)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	requireContains(t, "invalid function", response.Body.String(), "The workflow was not created", "Choose a function belonging to the selected app")
}

func TestWorkflowBuilderSavesAndRendersStepMappings(t *testing.T) {
	options := []workflowFunctionOption{{AppID: "A1", CallbackID: "triage", Title: "Triage"}}
	encoded, err := encodeWorkflowSteps(map[string]string{
		"step_count": "2", "step_1": "triage", "step_2": "triage",
		"mapping_2": `{"item":"inputs.item","prev":"steps.triage.outputs.x"}`,
	}, options, "A1")
	if err != nil {
		t.Fatal(err)
	}
	steps := decodeWorkflowSteps(encoded)
	if len(steps) != 2 || steps[0].Mapping != "" || steps[1].Mapping != `{"item":"inputs.item","prev":"steps.triage.outputs.x"}` {
		t.Fatalf("round-tripped steps=%+v from %s", steps, encoded)
	}
	if _, err := encodeWorkflowSteps(map[string]string{
		"step_1": "triage", "mapping_1": `not json`,
	}, options, "A1"); err == nil {
		t.Fatal("malformed mapping was accepted")
	}

	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Mapped triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	saved := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Mapped triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "mapping_1": {`{"item":"inputs.item"}`}, "action": {"publish"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("mapped publish=%d: %s", saved.Code, saved.Body)
	}
	page := get(t, mux, saved.Header().Get("Location"))
	requireContains(t, "rendered mapping", page.Body.String(), `name="mapping_1" value="{&#34;item&#34;:&#34;inputs.item&#34;}"`)
}

func TestWorkflowBuilderPreservesLongStepListsAndUsesRunPermissions(t *testing.T) {
	callbacks := make([]workflowDecodedStep, 12)
	fields := map[string]string{"step_count": "12"}
	for index := range callbacks {
		callbacks[index] = workflowDecodedStep{Callback: "triage"}
		fields["step_"+strconv.Itoa(index+1)] = "triage"
	}
	slots := workflowSlots(callbacks)
	if len(slots) != 13 || slots[11].Selected != "triage" {
		t.Fatalf("long workflow was truncated: %+v", slots)
	}
	encoded, err := encodeWorkflowSteps(fields, []workflowFunctionOption{{
		AppID: "A1", CallbackID: "triage", Title: "Triage",
	}}, "A1")
	if err != nil {
		t.Fatal(err)
	}
	var steps []map[string]string
	if err := json.Unmarshal([]byte(encoded), &steps); err != nil || len(steps) != 12 {
		t.Fatalf("steps=%v err=%v", steps, err)
	}
	principal := auth.Principal{WorkspaceID: "T1", UserID: "U2"}
	if workflowPermissionAllows(domain.AutomationPermission{PermissionType: "app_collaborators"}, principal, "U1") {
		t.Fatal("non-owner was shown an app-collaborator run action")
	}
	if !workflowPermissionAllows(domain.AutomationPermission{PermissionType: "named_entities", TeamIDs: []domain.WorkspaceID{"T1"}}, principal, "U1") {
		t.Fatal("workspace-named member was denied a run action")
	}
}

func TestWorkflowBuilderSavesAndRendersStepConditions(t *testing.T) {
	options := []workflowFunctionOption{{AppID: "A1", CallbackID: "triage", Title: "Triage"}}
	encoded, err := encodeWorkflowSteps(map[string]string{
		"step_count": "2", "step_1": "triage", "step_2": "triage",
		"condition_source_2": "inputs.severity", "condition_operator_2": "equals", "condition_value_2": "high",
	}, options, "A1")
	if err != nil {
		t.Fatal(err)
	}
	steps := decodeWorkflowSteps(encoded)
	if len(steps) != 2 || steps[1].ConditionSource != "inputs.severity" ||
		steps[1].ConditionOperator != "equals" || steps[1].ConditionValue != "high" {
		t.Fatalf("round-tripped steps=%+v from %s", steps, encoded)
	}
	if steps[0].ConditionSource != "" {
		t.Fatalf("unconditional step gained a condition: %+v", steps[0])
	}
	if _, err := encodeWorkflowSteps(map[string]string{
		"step_1": "triage", "condition_source_1": "inputs.severity",
	}, options, "A1"); err == nil {
		t.Fatal("condition source without an operator was accepted")
	}

	mux, csrf := seedWorkflowApp(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Conditional triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	saved := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Conditional triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "condition_source_1": {"inputs.severity"},
		"condition_operator_1": {"equals"}, "condition_value_1": {"high"}, "action": {"publish"},
	}.Encode(), false)
	if saved.Code != http.StatusSeeOther {
		t.Fatalf("conditional publish=%d: %s", saved.Code, saved.Body)
	}
	page := get(t, mux, saved.Header().Get("Location"))
	requireContains(t, "rendered condition", page.Body.String(),
		`name="condition_source_1" value="inputs.severity"`, `name="condition_value_1" value="high"`, `option value="equals" selected`)
}

// TestWorkflowPermissionsPanelControlsFindUseAndCopy walks the find/use/copy
// journey through the builder: the owner's permissions panel narrows each
// scope, and a member's page loses exactly the control the scope closes —
// the run button under "use", the copy control under "copy", and the whole
// page under "find" — while the underlying POSTs are refused, not just
// hidden.
func TestWorkflowPermissionsPanelControlsFindUseAndCopy(t *testing.T) {
	repository, mux, csrf := seedWorkflowAppWithStore(t)
	created := postForm(t, mux, "/app/workflows/create", url.Values{
		"_csrf": {csrf}, "title": {"Incident triage"}, "app_id": {"Aworkflow"}, "function_callback": {"triage"},
	}.Encode(), false)
	workflowURL := strings.Split(created.Header().Get("Location"), "?")[0]
	workflowID := domain.WorkflowID(strings.TrimPrefix(workflowURL, "/app/workflows/"))
	published := postForm(t, mux, workflowURL+"/update", url.Values{
		"_csrf": {csrf}, "version": {"1"}, "title": {"Incident triage"}, "input_schema": {`{}`},
		"step_1": {"triage"}, "action": {"publish"},
	}.Encode(), false)
	if published.Code != http.StatusSeeOther {
		t.Fatalf("publish=%d: %s", published.Code, published.Body)
	}
	triggered := postForm(t, mux, workflowURL+"/triggers", url.Values{
		"_csrf": {csrf}, "title": {"Start triage"}, "type": {"link"},
	}.Encode(), false)
	if triggered.Code != http.StatusSeeOther {
		t.Fatalf("create trigger=%d: %s", triggered.Code, triggered.Body)
	}
	page := get(t, mux, workflowURL)
	requireContains(t, "owner page", page.Body.String(),
		"Workflow permissions", "Who can find this workflow", "Who can use this workflow", "Who can copy this workflow")
	trigger := regexp.MustCompile(`/triggers/(Ft[0-9a-f]+)/run`).FindStringSubmatch(page.Body.String())
	if len(trigger) != 2 {
		t.Fatalf("owner page has no run form: %s", page.Body)
	}
	// Open the trigger to everyone so the workflow-level use scope is the only
	// gate left on the member's run button.
	if _, err := (service.Messages{Store: repository}).SetTriggerPermission(context.Background(), "T1", "U1", "Aworkflow",
		domain.WorkflowTriggerID(trigger[1]), domain.AutomationPermission{PermissionType: "everyone"}); err != nil {
		t.Fatal(err)
	}

	if err := repository.SeedSession(context.Background(), "member-session", domain.SessionRecord{
		WorkspaceID: "T1", UserID: "U2", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	memberCSRF := auth.CSRFToken("member-session")
	memberDo := func(method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		request := httptest.NewRequest(method, target, reader)
		if body != "" {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "member-session"})
		request.Header.Set(auth.CSRFTokenHeaderName, memberCSRF)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		return response
	}

	memberPage := memberDo(http.MethodGet, workflowURL, "")
	if memberPage.Code != http.StatusOK {
		t.Fatalf("member page=%d: %s", memberPage.Code, memberPage.Body)
	}
	requireContains(t, "member page with open use", memberPage.Body.String(), `<button type="submit">Run</button>`)
	requireMissing(t, "member page controls", memberPage.Body.String(), "Workflow permissions", "Copy workflow")
	if copied := memberDo(http.MethodPost, workflowURL+"/copy", url.Values{"_csrf": {memberCSRF}}.Encode()); copied.Code != http.StatusNotFound {
		t.Fatalf("member copy with copy closed=%d, want %d", copied.Code, http.StatusNotFound)
	}

	// The owner opens copy to everyone: the member gains the control and the
	// POST now succeeds.
	if opened := postForm(t, mux, workflowURL+"/permissions", url.Values{
		"_csrf": {csrf}, "scope": {"copy"}, "permission_type": {"everyone"},
	}.Encode(), false); opened.Code != http.StatusSeeOther {
		t.Fatalf("open copy=%d: %s", opened.Code, opened.Body)
	}
	memberPage = memberDo(http.MethodGet, workflowURL, "")
	requireContains(t, "member page with open copy", memberPage.Body.String(), "Copy workflow")
	copied := memberDo(http.MethodPost, workflowURL+"/copy", url.Values{"_csrf": {memberCSRF}}.Encode())
	if copied.Code != http.StatusSeeOther {
		t.Fatalf("member copy=%d: %s", copied.Code, copied.Body)
	}
	copyPage := memberDo(http.MethodGet, copied.Header().Get("Location"), "")
	requireContains(t, "member copy", copyPage.Body.String(), "Incident triage (copy)", "draft")

	// Narrowing use to somebody else removes the member's run button, and the
	// run POST is refused.
	if narrowed := postForm(t, mux, workflowURL+"/permissions", url.Values{
		"_csrf": {csrf}, "scope": {"use"}, "permission_type": {"named_entities"}, "user_ids": {"U3"},
	}.Encode(), false); narrowed.Code != http.StatusSeeOther {
		t.Fatalf("narrow use=%d: %s", narrowed.Code, narrowed.Body)
	}
	memberPage = memberDo(http.MethodGet, workflowURL, "")
	requireMissing(t, "member page with narrowed use", memberPage.Body.String(), `<button type="submit">Run</button>`)
	if run := memberDo(http.MethodPost, workflowURL+"/triggers/"+trigger[1]+"/run", url.Values{
		"_csrf": {memberCSRF}, "idempotency_key": {"member-run"},
	}.Encode()); run.Code < http.StatusBadRequest {
		t.Fatalf("member run with narrowed use=%d, want a refusal", run.Code)
	}

	// A permission the service rejects surfaces as a mutation error, not a 500.
	if invalid := postForm(t, mux, workflowURL+"/permissions", url.Values{
		"_csrf": {csrf}, "scope": {"share"}, "permission_type": {"everyone"},
	}.Encode(), false); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid scope=%d: %s", invalid.Code, invalid.Body)
	}

	// Narrowing find hides the workflow entirely: page and directory.
	if hidden := postForm(t, mux, workflowURL+"/permissions", url.Values{
		"_csrf": {csrf}, "scope": {"find"}, "permission_type": {"named_entities"}, "user_ids": {"U3"},
	}.Encode(), false); hidden.Code != http.StatusSeeOther {
		t.Fatalf("narrow find=%d: %s", hidden.Code, hidden.Body)
	}
	if memberPage = memberDo(http.MethodGet, workflowURL, ""); memberPage.Code != http.StatusNotFound {
		t.Fatalf("member page with narrowed find=%d, want %d", memberPage.Code, http.StatusNotFound)
	}
	directory := memberDo(http.MethodGet, "/app/workflows", "")
	requireMissing(t, "member directory with narrowed find", directory.Body.String(), string(workflowID))

	// The owner's own page still shows everything, including the stored scope.
	page = get(t, mux, workflowURL)
	requireContains(t, "owner page after narrowing", page.Body.String(), "Workflow permissions", "U3")
}
