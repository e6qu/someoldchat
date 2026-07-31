package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func seedWorkflowApp(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	repository, mux := browserWorkspace(t, auth.AllScopes())
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
	return mux, auth.CSRFToken("session")
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

func TestWorkflowBuilderPreservesLongStepListsAndUsesRunPermissions(t *testing.T) {
	callbacks := make([]string, 12)
	fields := map[string]string{"step_count": "12"}
	for index := range callbacks {
		callbacks[index] = "triage"
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
