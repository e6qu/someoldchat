package qualification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// opener yields one storage profile under test. Every backend the product can be
// configured with runs the same contract through runQualification, so a
// divergence between profiles fails the suite instead of being discovered in
// production.
type opener func(*testing.T, context.Context) (qualificationStore, func())

// restarter opens a *second* handle onto the same durable target the first was
// opened against, which is as close to losing the process as a test in this
// package can get: the old handle is closed, every in-memory structure it held
// is dropped, and the new one has nothing but what was committed to disk.
//
// It exists because the suite could not tell a durable write from a cached one.
// Every contract here opened its target once and read back through the same
// handle, so "durable" meant "committed and readable", not "survives the
// process that wrote it". The reopen tests that did check that lived in
// internal/store/sqlstore and were SQLite-only — which left PostgreSQL, the
// profile the production Terraform requires, with no restart coverage at all.
//
// A profile that cannot outlive its process returns nil and is skipped with a
// reason rather than silently passing a contract it cannot honour.
type restarter func(*testing.T, context.Context) qualificationStore

// restartOpener yields a profile together with the means to reopen it.
type restartOpener func(*testing.T, context.Context) (qualificationStore, restarter, func())

// runRestartQualification is the crash-only gate specs/persistence.md asks for:
// "Committed state MUST survive a process or node crash". Each contract writes
// through one handle, drops it, and reads through a new one.
func runRestartQualification(t *testing.T, open restartOpener) {
	t.Helper()
	for _, contract := range []struct {
		name string
		run  func(*testing.T, restartOpener)
	}{
		{"committed conversation state survives a restart", committedStateSurvivesARestart},
		{"unfinished outbox work is claimable after a restart", unfinishedOutboxWorkSurvivesARestart},
		{"policy and lifecycle state survive a restart", policyStateSurvivesARestart},
	} {
		t.Run(contract.name, func(t *testing.T) { contract.run(t, open) })
	}
}

func runQualification(t *testing.T, open opener) {
	t.Helper()
	for _, contract := range []struct {
		name string
		run  func(*testing.T, opener)
	}{
		{"core repository", coreRepositoryContract},
		{"drafts and sent repository", draftsAndSentRepositoryContract},
		{"OpenID refresh token rotation is durable", openIDRefreshTokenRotationIsDurable},
		{"lists repository", listsRepositoryContract},
		{"workflow automation repository", workflowAutomationRepositoryContract},
		{"published wave one repository", publishedWaveOneRepositoryContract},
		{"published integration repository", publishedIntegrationRepositoryContract},
		{"durable event delivery repository", durableEventDeliveryRepositoryContract},
		{"message order is chronological", messageOrderIsChronological},
		{"unread count follows the read cursor", unreadCountFollowsTheReadCursor},
		{"batch read cursors agree with the newest message", batchReadCursorsAgreeWithTheNewestMessage},
		{"followed threads agree across profiles", followedThreadsAgreeAcrossProfiles},
		{"followed threads survive more roots than one chunk", followedThreadsSurviveMoreRootsThanOneChunk},
		{"workflow delays wait on a durable instant", workflowDelaysWaitOnADurableInstant},
		{"create message validates and is referential", createMessageValidatesAndIsReferential},
		{"expired outbox lease is fenced", expiredOutboxLeaseIsFenced},
		{"internal topics stay internal", internalTopicsStayInternal},
		{"events retain their actor", eventsRetainTheirActor},
		{"email identity is case folded", emailIdentityIsCaseFolded},
		{"conversation search treats metacharacters literally", conversationSearchTreatsMetacharactersLiterally},
		{"search folds Unicode identically", searchFoldsUnicodeIdentically},
		{"recent searches are private ordered and deduplicated", recentSearchesArePrivateOrderedAndDeduplicated},
		{"user group mentions create visibility safe activity", userGroupMentionsCreateVisibilitySafeActivity},
		{"messages page in both directions", messagesPageInBothDirections},
		{"referential failures are sentinels", referentialFailuresAreSentinels},
		{"expired Socket Mode connection is not revived", expiredSocketModeConnectionIsNotRevived},
		{"Socket Mode batches are all or nothing", socketModeBatchesAreAllOrNothing},
		{"seed helpers reject invalid input", seedHelpersRejectInvalidInput},
		{"Socket Mode admission is atomic under concurrency", socketModeAdmissionIsAtomicUnderConcurrency},
		{"blob references tolerate an arbitrary profile photo URL", blobReferencesTolerateAnArbitraryProfilePhotoURL},
		{"email identity is not Unicode case folded", emailIdentityIsNotUnicodeCaseFolded},
		{"stars page in chronological order", starsPageInChronologicalOrder},
		{"messages resolve by their own creation instant", messagesResolveByTheirOwnCreationInstant},
		{"lists are created with their items or not at all", listsAreCreatedWithTheirItemsOrNotAtAll},
		{"profile changes commit with every event they carry", profileChangesCommitWithEveryEventTheyCarry},
		{"resolved access names one grant deterministically", resolvedAccessNamesOneGrantDeterministically},
		{"mutations return the value they wrote", mutationsReturnTheValueTheyWrote},
		{"ending an already ended call is a conflict", endingAnAlreadyEndedCallIsAConflict},
		{"connected channel pages are filtered and bounded", connectedChannelPagesAreFilteredAndBounded},
		{"message timestamps are unique per conversation", messageTimestampsAreUniquePerConversation},
		{"the creator of a conversation is a member of it", conversationCreatorIsAMember},
		{"an unconfigured auth method is enabled", authMethodDefaultsToEnabled},
		{"revoking an app token announces tokens_revoked once", revokingAnAppTokenAnnouncesTokensRevokedOnce},
		{"the uninstall announcement outlives the installation", uninstallAnnouncementOutlivesTheInstallation},
		{"a conversation change and its notice commit together", conversationNoticesCommitWithTheirChange},
		{"thread summaries are batched and identical across profiles", threadSummariesAreBatchedAndIdentical},
		{"activity follows the read cursor in both directions", activityFollowsTheReadCursorBothWays},
		{"a deleted file is deleted on every message that carries it", aDeletedFileIsDeletedOnEveryMessageThatCarriesIt},
		{"deleting the last carrier retracts the file share", deletingTheLastCarrierRetractsTheFileShare},
		{"accepting an invitation commits the whole membership", acceptingAnInvitationCommitsTheWholeMembership},
		{"workspace analytics count the same on every profile", workspaceAnalyticsCountTheSameOnEveryProfile},
		{"huddles converge and end with their last participant", huddlesConvergeAndEndWithTheirLastParticipant},
		{"workspaces for an address agree on every profile", workspacesForAnAddressAgreeOnEveryProfile},
		{"Slack Connect capacity is claimed transactionally", slackConnectCapacityIsClaimedTransactionally},
		{"retention deletes the same content on every profile", retentionDeletesTheSameContentOnEveryProfile},
		{"retention sweeps are claimed exactly once", retentionSweepsAreClaimedExactlyOnce},
		{"conversation retention overrides the workspace default", conversationRetentionOverridesTheWorkspaceDefault},
		{"typing signals expire without being retracted", typingSignalsExpireWithoutBeingRetracted},
		{"canvas search folds text and stops at the reader's access", canvasSearchFoldsTextAndStopsAtAccess},
		{"directory search folds names on every profile", directorySearchFoldsNamesOnEveryProfile},
		{"a file description survives and belongs to its uploader", fileDescriptionBelongsToItsUploader},
		{"a canvas share reaches Activity on every profile", canvasShareReachesActivity},
		{"a notification schedule round-trips on every profile", notificationScheduleRoundTrips},
		{"an assigned list item reaches Activity on every profile", listAssignmentReachesActivity},
		{"deleting a list item is all or nothing and survives in Activity", deletingAListItemIsAllOrNothingAndSurvivesInActivity},
		{"removing a list column takes its cells with it", removingAListColumnTakesItsCellsWithIt},
		{"external connections are derived and end everywhere", externalConnectionsAreDerivedAndEndEverywhere},
		{"sessions are listed without their tokens", sessionsAreListedWithoutTheirTokens},
		{"stopping a workflow is not an edit", stoppingAWorkflowIsNotAnEdit},
		{"a channel converts both ways and says which kind it is not", aChannelConvertsBothWaysAndSaysWhichKindItIsNot},
		{"search modifiers mean the same on every profile", searchModifiersMeanTheSame},
		{"canvas revisions record what was replaced", canvasRevisionsRecordWhatWasReplaced},
		{"canvas comments outlive the section they annotate", canvasCommentsOutliveTheirSection},
		{"a conversation has exactly one canvas", aConversationHasExactlyOneCanvas},
		{"canvas grants are listed in one stable order", canvasGrantsAreListedInOneStableOrder},
		{"list grants are listed in one stable order", listGrantsAreListedInOneStableOrder},
		{"a Slack Connect decision reaches its requester", connectDecisionReachesItsRequester},
	} {
		t.Run(contract.name, func(t *testing.T) { contract.run(t, open) })
	}
}

func userGroupMentionsCreateVisibilitySafeActivity(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspace := domain.WorkspaceID("T-group-mention-" + suffix)
	author := domain.UserID("U-group-author-" + suffix)
	inside := domain.UserID("U-group-inside-" + suffix)
	outside := domain.UserID("U-group-outside-" + suffix)
	public := domain.ConversationID("C-group-public-" + suffix)
	private := domain.ConversationID("C-group-private-" + suffix)
	groupID := domain.UserGroupID("SQUAL" + suffix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, seed := range []func() error{
		func() error {
			return repository.SeedWorkspace(ctx, domain.Workspace{ID: workspace, Name: "Group mention qualification"})
		},
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: author, WorkspaceID: workspace, Name: "author"})
		},
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: inside, WorkspaceID: workspace, Name: "inside"})
		},
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: outside, WorkspaceID: workspace, Name: "outside"})
		},
		func() error {
			return repository.SeedConversation(ctx, domain.Conversation{ID: public, WorkspaceID: workspace, Name: "public"})
		},
		func() error {
			return repository.SeedConversation(ctx, domain.Conversation{ID: private, WorkspaceID: workspace, Name: "private", Kind: domain.ConversationTypePrivate})
		},
		func() error { return repository.SeedConversationMember(ctx, public, author) },
		func() error { return repository.SeedConversationMember(ctx, public, inside) },
		func() error { return repository.SeedConversationMember(ctx, private, author) },
		func() error { return repository.SeedConversationMember(ctx, private, inside) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	event := func(id, topic string, at time.Time) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspace, Topic: topic, CreatedAt: at}
	}
	group := domain.UserGroup{
		ID: groupID, WorkspaceID: workspace, Name: "Support rotation", Handle: "support",
		Creator: author, UpdatedBy: author, CreatedAt: now, UpdatedAt: now, Enabled: true,
	}
	if err := repository.CreateUserGroup(ctx, group, event("group", "usergroup.created", now)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserGroupUsers(ctx, workspace, groupID, []domain.UserID{inside, outside}, author, event("group-users", "usergroup.users_changed", now)); err != nil {
		t.Fatal(err)
	}
	create := func(id string, channel domain.ConversationID, at time.Time) {
		t.Helper()
		message := domain.Message{
			ID: domain.MessageID(id + "-" + suffix), WorkspaceID: workspace, Conversation: channel, AuthorID: author,
			Text: "please review <!subteam^" + string(groupID) + ">", CreatedAt: at,
		}
		if err := repository.CreateMessage(ctx, message, event("event-"+id, "message.created", at), ""); err != nil {
			t.Fatal(err)
		}
	}
	mentions := func(user domain.UserID) domain.ActivityPage {
		t.Helper()
		page, err := repository.ListActivity(ctx, workspace, user, domain.ActivityQuery{
			Kinds: []domain.ActivityKind{domain.ActivityMention}, Page: domain.PageRequest{Limit: 10},
		})
		if err != nil {
			t.Fatal(err)
		}
		return page
	}

	create("public-group-mention", public, now.Add(time.Second))
	for _, user := range []domain.UserID{inside, outside} {
		page := mentions(user)
		if len(page.Items) != 1 || !page.Items[0].SourceAvailable || page.Items[0].Conversation != public {
			t.Fatalf("public group mention for %s = %+v", user, page)
		}
	}

	if err := repository.SetUserGroupEnabled(ctx, workspace, groupID, false, author, event("group-disable", "usergroup.enabled_changed", now.Add(2*time.Second))); err != nil {
		t.Fatal(err)
	}
	create("disabled-group-mention", public, now.Add(3*time.Second))
	if page := mentions(inside); len(page.Items) != 1 {
		t.Fatalf("disabled group created activity: %+v", page)
	}

	if err := repository.SetUserGroupEnabled(ctx, workspace, groupID, true, author, event("group-enable", "usergroup.enabled_changed", now.Add(4*time.Second))); err != nil {
		t.Fatal(err)
	}
	create("private-group-mention", private, now.Add(5*time.Second))
	if page := mentions(inside); len(page.Items) != 2 || !page.Items[0].SourceAvailable || page.Items[0].Conversation != private {
		t.Fatalf("private group member activity = %+v", page)
	}
	if page := mentions(outside); len(page.Items) != 1 {
		t.Fatalf("private group mention leaked to non-member: %+v", page)
	}
}

func recentSearchesArePrivateOrderedAndDeduplicated(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-recent-search-" + suffix)
	aliceID := domain.UserID("U-recent-search-alice-" + suffix)
	bobID := domain.UserID("U-recent-search-bob-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Recent search qualification"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: aliceID, WorkspaceID: workspaceID, Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: bobID, WorkspaceID: workspaceID, Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
	for _, value := range []domain.SearchHistoryEntry{
		{WorkspaceID: workspaceID, UserID: aliceID, Query: "first", SearchedAt: base},
		{WorkspaceID: workspaceID, UserID: aliceID, Query: "second", SearchedAt: base.Add(time.Minute)},
		{WorkspaceID: workspaceID, UserID: aliceID, Query: "first", SearchedAt: base.Add(2 * time.Minute)},
		{WorkspaceID: workspaceID, UserID: bobID, Query: "private-to-bob", SearchedAt: base.Add(3 * time.Minute)},
	} {
		if err := repository.RecordSearchHistory(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	values, err := repository.ListSearchHistory(ctx, workspaceID, aliceID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values[0].Query != "first" || values[1].Query != "second" {
		t.Fatalf("recent searches=%+v, want refreshed first then second", values)
	}
}

func draftsAndSentRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspace := domain.WorkspaceID("T-drafts-" + suffix)
	user := domain.UserID("U-drafts-" + suffix)
	channel := domain.ConversationID("C-drafts-" + suffix)
	now := time.Now().UTC().Truncate(time.Second)
	for _, seed := range []func() error{
		func() error { return repository.SeedWorkspace(ctx, domain.Workspace{ID: workspace, Name: "Drafts"}) },
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: user, WorkspaceID: workspace, Name: "author"})
		},
		func() error {
			return repository.SeedConversation(ctx, domain.Conversation{ID: channel, WorkspaceID: workspace, Name: "general"})
		},
		func() error { return repository.SeedConversationMember(ctx, channel, user) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	event := func(id, topic string) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspace, Topic: topic, CreatedAt: now}
	}
	draft := domain.Draft{WorkspaceID: workspace, UserID: user, ConversationID: channel, Text: "unfinished", UpdatedAt: now}
	if _, err := repository.UpsertDraft(ctx, draft, event("draft-save", "draft.saved")); err != nil {
		t.Fatal(err)
	}
	storedDraft, err := repository.GetDraft(ctx, workspace, user, channel, "")
	if err != nil || storedDraft.Text != draft.Text {
		t.Fatalf("draft=%+v err=%v", storedDraft, err)
	}
	drafts, err := repository.ListDrafts(ctx, workspace, user, domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(drafts.Items) != 1 {
		t.Fatalf("drafts=%+v err=%v", drafts, err)
	}
	message := domain.Message{ID: domain.MessageID("M-drafts-" + suffix), WorkspaceID: workspace, Conversation: channel, AuthorID: user, Text: "sent", CreatedAt: now.Add(time.Second)}
	if err := repository.CreateMessage(ctx, message, event("message-create", "message.created"), ""); err != nil {
		t.Fatal(err)
	}
	sent, err := repository.ListAuthoredMessages(ctx, workspace, user, domain.PageRequest{Limit: 10, Descending: true})
	if err != nil || len(sent.Messages) != 1 || sent.Messages[0].ID != message.ID {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	credential := "first-party-" + suffix
	scheduled := domain.ScheduledMessage{
		WorkspaceID: workspace, ID: domain.ScheduledMessageID("Q-drafts-" + suffix), Channel: channel,
		Author: user, CredentialHash: credential, Text: "before", PostAt: now.Add(2 * time.Hour), CreatedAt: now,
	}
	if err := repository.CreateScheduledMessageWithinLimit(ctx, scheduled, 5*time.Minute, 30, event("schedule-create", "message.scheduled")); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateScheduledMessageWithinLimit(ctx, domain.ScheduledMessageUpdate{
		WorkspaceID: workspace, ID: scheduled.ID, Channel: channel, CredentialHash: credential,
		Text: "after", PostAt: now.Add(3 * time.Hour),
	}, 5*time.Minute, 30, event("schedule-update", "message.schedule_updated"))
	if err != nil || updated.Text != "after" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	failedClaim, err := repository.ClaimScheduledMessageForCredential(ctx, workspace, credential, scheduled.ID, "worker-"+suffix, time.Minute)
	if err != nil || failedClaim.ID != scheduled.ID {
		t.Fatalf("worker claim=%+v err=%v", failedClaim, err)
	}
	if err := repository.MarkScheduledMessageFailed(ctx, "worker-"+suffix, scheduled.ID, "not_in_channel", now.Add(time.Minute), event("schedule-failed", "message.schedule_failed")); err != nil {
		t.Fatal(err)
	}
	claimed, err := repository.ClaimScheduledMessageForCredential(ctx, workspace, credential, scheduled.ID, "send-now-"+suffix, time.Minute)
	if err != nil || claimed.ID != scheduled.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := repository.MarkScheduledMessageDelivered(ctx, "send-now-"+suffix, scheduled.ID); err != nil {
		t.Fatal(err)
	}
	history, err := repository.ListScheduledMessageHistory(ctx, workspace, credential, true, domain.PageRequest{Limit: 10})
	if err != nil || len(history.Items) != 1 || history.Items[0].DeliveredAt.IsZero() || !history.Items[0].FailedAt.IsZero() || history.Items[0].FailureCode != "" {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	postedSchedule := domain.ScheduledMessage{
		WorkspaceID: workspace, ID: domain.ScheduledMessageID("Q-posted-" + suffix), Channel: channel,
		Author: user, CredentialHash: credential, Text: "commit before acknowledgement", PostAt: now.Add(4 * time.Hour), CreatedAt: now,
	}
	if err := repository.CreateScheduledMessage(ctx, postedSchedule, event("schedule-post-create", "message.scheduled")); err != nil {
		t.Fatal(err)
	}
	postedMessage := domain.Message{
		ID: domain.MessageID("M-posted-" + suffix), WorkspaceID: workspace, Conversation: channel,
		AuthorID: user, Text: postedSchedule.Text, CreatedAt: now.Add(2 * time.Second),
	}
	if err := repository.CreateScheduledMessagePost(ctx, postedSchedule.ID, postedMessage, event("schedule-post-message", "message.created")); err != nil {
		t.Fatal(err)
	}
	cached, err := repository.GetIdempotentMessage(ctx, workspace, user, string(postedSchedule.ID))
	if err != nil || cached.ID != postedMessage.ID {
		t.Fatalf("scheduled idempotent message=%+v err=%v", cached, err)
	}
	if err := repository.DeleteScheduledMessageForCredential(ctx, workspace, credential, channel, postedSchedule.ID, event("schedule-post-delete", "message.schedule_deleted")); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("committed schedule was cancelled before acknowledgement: %v", err)
	}
	if err := repository.DeleteDraft(ctx, workspace, user, channel, "", event("draft-delete", "draft.deleted")); err != nil {
		t.Fatal(err)
	}
}

type qualificationStore interface {
	store.Store
	SeedAppToken(context.Context, string, domain.AppTokenRecord) error
	SeedToken(context.Context, string, domain.TokenRecord) error
	SeedWorkspace(context.Context, domain.Workspace) error
	SeedUser(context.Context, domain.User) error
	SeedConversation(context.Context, domain.Conversation) error
	SeedConversationMember(context.Context, domain.ConversationID, domain.UserID) error
	// SeedSession writes a session as stored rather than as created, which is
	// the only way to arrange the states a session administrator has to be able
	// to see through: one already expired, and one revoked.
	SeedSession(context.Context, string, domain.SessionRecord) error
}

func workflowAutomationRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-workflow-" + suffix)
	userID := domain.UserID("U-workflow-" + suffix)
	conversationID := domain.ConversationID("C-workflow-" + suffix)
	workflowID := domain.WorkflowID("Wf" + suffix)
	triggerID := domain.WorkflowTriggerID("Ft" + suffix)
	runID := domain.WorkflowRunID("Wx" + suffix)
	stepID := domain.WorkflowStepID("Fx" + suffix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	event := func(id, topic string, at time.Time) events.Event {
		return events.Event{
			ID: domain.EventID("E-workflow-" + id + "-" + suffix), WorkspaceID: workspaceID,
			ActorID: userID, Topic: topic, CreatedAt: at,
		}
	}
	for _, seed := range []func() error{
		func() error {
			return repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Workflow qualification"})
		},
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: userID, WorkspaceID: workspaceID, Name: "workflow-owner"})
		},
		func() error {
			return repository.SeedConversation(ctx, domain.Conversation{ID: conversationID, WorkspaceID: workspaceID, Name: "workflow-channel"})
		},
		func() error { return repository.SeedConversationMember(ctx, conversationID, userID) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}

	workflow := domain.WorkflowDefinition{
		ID: workflowID, WorkspaceID: workspaceID, AppID: "A-workflow", OwnerID: userID,
		CallbackID: "triage", Title: "Triage", Description: "Durable workflow", Icon: "🚨",
		InputSchema: `{"type":"object"}`, Steps: `[{"function_id":"triage"}]`,
		Status: domain.WorkflowDraft, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflow(ctx, workflow, event("create", "workflow.created", now)); err != nil {
		t.Fatal(err)
	}
	created, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || created.Version != 1 || created.Status != domain.WorkflowDraft || created.Icon != "🚨" {
		t.Fatalf("created workflow=%+v err=%v", created, err)
	}
	workflow.Title = "Published triage"
	workflow.Icon = "📋"
	workflow.Status = domain.WorkflowPublished
	workflow.PublishedVersion = 2
	workflow.Version = 2
	workflow.UpdatedAt = now.Add(time.Second)
	if err := repository.UpdateWorkflow(ctx, workflow, event("publish", "workflow.published", workflow.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateWorkflow(ctx, workflow, event("stale", "workflow.updated", workflow.UpdatedAt)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale workflow update error=%v, want ErrConflict", err)
	}
	revisions, err := repository.ListWorkflowRevisions(ctx, workspaceID, workflowID)
	if err != nil || len(revisions) != 2 || revisions[0].Version != 1 || revisions[1].Version != 2 {
		t.Fatalf("workflow revisions=%+v err=%v", revisions, err)
	}
	if revisions[1].Title != "Published triage" || revisions[1].Description != "Durable workflow" ||
		revisions[1].Icon != "📋" ||
		revisions[1].InputSchema != `{"type":"object"}` || revisions[1].CallbackID != "triage" {
		t.Fatalf("workflow revision metadata=%+v", revisions[1])
	}
	if revisions[0].Icon != "🚨" {
		t.Fatalf("draft revision icon=%q, want 🚨", revisions[0].Icon)
	}

	// Managers are workflow-level metadata: a dedicated op replaces the list,
	// and a content update carries it forward rather than clearing it.
	if err := repository.SetWorkflowManagers(ctx, workspaceID, workflowID, []domain.UserID{userID, domain.UserID("U-manager-" + suffix)},
		event("managers", "workflow.managers_set", now)); err != nil {
		t.Fatal(err)
	}
	withManagers, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || len(withManagers.ManagerIDs) != 2 {
		t.Fatalf("workflow with managers=%+v err=%v", withManagers, err)
	}
	edited := workflow
	edited.Icon = "🔁"
	edited.Version = withManagers.Version + 1
	edited.UpdatedAt = now.Add(3 * time.Second)
	if err := repository.UpdateWorkflow(ctx, edited, event("icon-edit", "workflow.updated", edited.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	afterEdit, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || len(afterEdit.ManagerIDs) != 2 || afterEdit.Icon != "🔁" {
		t.Fatalf("managers after content edit=%+v err=%v", afterEdit, err)
	}
	// Revert the staged edit so the rest of the qualification proceeds from the
	// published revision it already set up.
	if _, err := repository.DiscardWorkflowStagedChanges(ctx, workspaceID, workflowID, afterEdit.Version, event("icon-revert", "workflow.staged_discarded", edited.UpdatedAt)); err != nil {
		t.Fatal(err)
	}

	trigger := domain.WorkflowTrigger{
		ID: triggerID, WorkflowID: workflowID, WorkspaceID: workspaceID, AppID: workflow.AppID,
		Title: "Run triage", Type: "link", Config: `{}`, Enabled: true,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SetWorkflowTrigger(ctx, trigger, event("trigger", "workflow.trigger_created", now)); err != nil {
		t.Fatal(err)
	}
	otherWorkflow := workflow
	otherWorkflow.ID = domain.WorkflowID(string(workflowID) + "-other")
	otherWorkflow.CallbackID = "triage-other-" + suffix
	otherWorkflow.Status = domain.WorkflowDraft
	otherWorkflow.PublishedVersion = 0
	otherWorkflow.UpdatedAt = now.Add(2 * time.Second)
	if err := repository.CreateWorkflow(ctx, otherWorkflow, event("other-workflow", "workflow.created", otherWorkflow.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	movedTrigger := trigger
	movedTrigger.WorkflowID = otherWorkflow.ID
	movedTrigger.Version = 2
	movedTrigger.UpdatedAt = now.Add(3 * time.Second)
	if err := repository.SetWorkflowTrigger(ctx, movedTrigger, event("move-trigger", "workflow.trigger_updated", movedTrigger.UpdatedAt)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("trigger moved across workflows: %v", err)
	}
	triggers, err := repository.ListWorkflowTriggers(ctx, workspaceID, workflowID)
	if err != nil || len(triggers) != 1 || triggers[0].Version != 1 || !triggers[0].Enabled {
		t.Fatalf("workflow triggers=%+v err=%v", triggers, err)
	}
	permission := domain.AutomationPermission{
		ResourceType: "trigger", ResourceID: string(triggerID), WorkspaceID: workspaceID,
		AppID: workflow.AppID, PermissionType: "named_entities", UserIDs: []domain.UserID{userID},
		ChannelIDs: []domain.ConversationID{conversationID}, UpdatedAt: now,
	}
	if err := repository.SetAutomationPermission(ctx, permission, event("permission", "workflow.trigger_permission_set", now)); err != nil {
		t.Fatal(err)
	}
	storedPermission, err := repository.GetAutomationPermission(ctx, workspaceID, "trigger", string(triggerID))
	if err != nil || storedPermission.PermissionType != "named_entities" ||
		len(storedPermission.UserIDs) != 1 || storedPermission.UserIDs[0] != userID ||
		len(storedPermission.ChannelIDs) != 1 || storedPermission.ChannelIDs[0] != conversationID {
		t.Fatalf("workflow permission=%+v err=%v", storedPermission, err)
	}
	// The workflow-level find/use/copy scopes ride the same storage with their
	// own resource types; each scope is addressed independently.
	scopePermission := domain.AutomationPermission{
		ResourceType: "workflow_copy", ResourceID: string(workflowID), WorkspaceID: workspaceID,
		AppID: workflow.AppID, PermissionType: "everyone", UpdatedAt: now,
	}
	if err := repository.SetAutomationPermission(ctx, scopePermission, event("scope-permission", "workflow.permission_set", now)); err != nil {
		t.Fatal(err)
	}
	storedScope, err := repository.GetAutomationPermission(ctx, workspaceID, "workflow_copy", string(workflowID))
	if err != nil || storedScope.PermissionType != "everyone" || storedScope.ResourceType != "workflow_copy" {
		t.Fatalf("workflow copy scope=%+v err=%v", storedScope, err)
	}
	if _, err := repository.GetAutomationPermission(ctx, workspaceID, "workflow_find", string(workflowID)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unset workflow find scope error=%v, want ErrNotFound", err)
	}
	if err := repository.SetFeaturedWorkflows(ctx, workspaceID, conversationID, []domain.FeaturedWorkflow{{
		TriggerID: triggerID, Title: trigger.Title,
	}}, event("featured", "workflow.featured_set", now)); err != nil {
		t.Fatal(err)
	}
	featured, err := repository.ListFeaturedWorkflows(ctx, workspaceID, []domain.ConversationID{conversationID})
	if err != nil || len(featured) != 1 || featured[0].TriggerID != triggerID ||
		featured[0].WorkspaceID != workspaceID || featured[0].ConversationID != conversationID {
		t.Fatalf("featured workflows=%+v err=%v", featured, err)
	}

	run := domain.WorkflowRun{
		ID: runID, WorkflowID: workflowID, WorkflowVersion: 2, TriggerID: triggerID,
		WorkspaceID: workspaceID, AppID: workflow.AppID, ActorID: userID,
		ConversationID: conversationID, Status: domain.WorkflowRunRunning,
		Inputs: `{"item":"incident"}`, Outputs: `{}`, IdempotencyKey: "workflow-once-" + suffix,
		CreatedAt: now, UpdatedAt: now,
	}
	step := domain.WorkflowStep{
		ID: stepID, WorkflowRunID: runID, WorkspaceID: workspaceID, AppID: workflow.AppID,
		UserID: userID, FunctionID: "FnTriage", EditID: "triage", Status: domain.WorkflowStepExecuting,
		Inputs: run.Inputs, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflowRun(ctx, run, &step, []events.Event{
		event("run", "workflow.run_started", now),
		event("step", "function_executed", now),
	}); err != nil {
		t.Fatal(err)
	}
	duplicate := run
	duplicate.ID = domain.WorkflowRunID("Wx-duplicate-" + suffix)
	if err := repository.CreateWorkflowRun(ctx, duplicate, nil, []events.Event{
		event("duplicate", "workflow.run_started", now),
	}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate workflow run error=%v, want ErrAlreadyExists", err)
	}
	idempotentRun, err := repository.GetWorkflowRunByIdempotency(ctx, workspaceID, run.IdempotencyKey)
	if err != nil || idempotentRun.ID != runID {
		t.Fatalf("idempotent workflow run=%+v err=%v", idempotentRun, err)
	}
	step.Status = domain.WorkflowStepCompleted
	step.Outputs = `{"result":"ok"}`
	step.UpdatedAt = now.Add(2 * time.Second)
	run.Status = domain.WorkflowRunCompleted
	run.Outputs = step.Outputs
	run.CurrentStep = 1
	run.UpdatedAt = step.UpdatedAt
	run.CompletedAt = step.UpdatedAt
	if err := repository.AdvanceWorkflowRun(ctx, step, nil, run, 0, []events.Event{
		event("complete", "workflow.run_completed", run.CompletedAt),
	}); err != nil {
		t.Fatal(err)
	}
	storedRun, err := repository.GetWorkflowRun(ctx, workspaceID, runID)
	if err != nil || storedRun.Status != domain.WorkflowRunCompleted ||
		storedRun.Outputs != run.Outputs || storedRun.CurrentStep != 1 ||
		storedRun.WorkflowVersion != 2 || storedRun.CompletedAt.IsZero() {
		t.Fatalf("completed workflow run=%+v err=%v", storedRun, err)
	}
	storedStep, err := repository.GetWorkflowStep(ctx, workspaceID, stepID)
	if err != nil || storedStep.Status != domain.WorkflowStepCompleted || storedStep.Outputs != step.Outputs {
		t.Fatalf("completed workflow step=%+v err=%v", storedStep, err)
	}
	if runSteps, err := repository.ListWorkflowRunSteps(ctx, workspaceID, runID); err != nil || len(runSteps) != 1 || runSteps[0].ID != stepID {
		t.Fatalf("run steps=%+v err=%v", runSteps, err)
	}

	// Staged edits can be discarded: the head reverts to the published revision.
	published, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || published.Status != domain.WorkflowPublished || published.Version != published.PublishedVersion {
		t.Fatalf("published head=%+v err=%v", published, err)
	}
	stagedWorkflow := published
	stagedWorkflow.Title = "Staged and discarded"
	stagedWorkflow.Icon = "🗑"
	stagedWorkflow.Version = published.Version + 1
	stagedWorkflow.UpdatedAt = now.Add(5 * time.Second)
	if err := repository.UpdateWorkflow(ctx, stagedWorkflow, event("staged", "workflow.updated", stagedWorkflow.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	stagedHead, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || stagedHead.Title != "Staged and discarded" || stagedHead.Icon != "🗑" || stagedHead.Version == stagedHead.PublishedVersion {
		t.Fatalf("staged head=%+v err=%v", stagedHead, err)
	}
	discarded, err := repository.DiscardWorkflowStagedChanges(ctx, workspaceID, workflowID, stagedHead.Version, event("discard", "workflow.staged_discarded", stagedWorkflow.UpdatedAt))
	if err != nil || !discarded {
		t.Fatalf("discard changed=%v err=%v", discarded, err)
	}
	afterDiscard, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil || afterDiscard.Title != workflow.Title || afterDiscard.Icon != workflow.Icon || afterDiscard.Version != workflow.PublishedVersion {
		t.Fatalf("after discard=%+v err=%v", afterDiscard, err)
	}

	// Unpublishing cancels running runs and their executing steps atomically.
	cancellableRun := domain.WorkflowRun{
		ID: domain.WorkflowRunID("Wx-cancel-" + suffix), WorkflowID: workflowID, WorkflowVersion: 2,
		TriggerID: triggerID, WorkspaceID: workspaceID, AppID: workflow.AppID, ActorID: userID,
		Status: domain.WorkflowRunRunning, Inputs: `{}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	cancellableStep := domain.WorkflowStep{
		ID: domain.WorkflowStepID("Fx-cancel-" + suffix), WorkflowRunID: cancellableRun.ID,
		WorkspaceID: workspaceID, AppID: workflow.AppID, UserID: userID, FunctionID: "FnCancel",
		EditID: "triage", Status: domain.WorkflowStepExecuting, Inputs: `{}`, Outputs: `{}`,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflowRun(ctx, cancellableRun, &cancellableStep, []events.Event{
		event("cancel-run", "workflow.run_started", now),
	}); err != nil {
		t.Fatal(err)
	}
	unpublished := workflow
	unpublished.Status = domain.WorkflowDisabled
	unpublished.Version = afterDiscard.Version + 1
	unpublished.UpdatedAt = now.Add(6 * time.Second)
	if err := repository.UpdateWorkflow(ctx, unpublished, event("unpublish", "workflow.unpublished", unpublished.UpdatedAt)); err != nil {
		t.Fatal(err)
	}
	cancelledRun, err := repository.GetWorkflowRun(ctx, workspaceID, cancellableRun.ID)
	if err != nil || cancelledRun.Status != domain.WorkflowRunCancelled || cancelledRun.Error != "workflow_unpublished" {
		t.Fatalf("run after unpublish=%+v err=%v", cancelledRun, err)
	}
	cancelledStep, err := repository.GetWorkflowStep(ctx, workspaceID, cancellableStep.ID)
	if err != nil || cancelledStep.Status != domain.WorkflowStepCancelled {
		t.Fatalf("step after unpublish=%+v err=%v", cancelledStep, err)
	}

	occurrence := now.Add(time.Hour)
	scheduledTrigger := domain.WorkflowTrigger{
		ID: domain.WorkflowTriggerID("Ft-scheduled-" + suffix), WorkflowID: workflowID, WorkspaceID: workspaceID,
		AppID: workflow.AppID, Title: "Hourly triage", Type: "scheduled",
		Config:  `{"start_time":"2026-01-01T00:00:00Z","timezone":"UTC","frequency":{"type":"hourly"}}`,
		Enabled: true, NextRunAt: occurrence, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SetWorkflowTrigger(ctx, scheduledTrigger, event("scheduled-trigger", "workflow.trigger_created", now)); err != nil {
		t.Fatal(err)
	}
	storedScheduled, err := repository.GetWorkflowTrigger(ctx, workspaceID, scheduledTrigger.ID)
	if err != nil || !storedScheduled.NextRunAt.Equal(occurrence) {
		t.Fatalf("scheduled trigger next_run_at=%s err=%v, want %s", storedScheduled.NextRunAt, err, occurrence)
	}
	if due, err := repository.DueScheduledWorkflowTriggers(ctx, workspaceID, occurrence.Add(-time.Second), 10); err != nil || len(due) != 0 {
		t.Fatalf("early due triggers=%+v err=%v", due, err)
	}
	due, err := repository.DueScheduledWorkflowTriggers(ctx, workspaceID, occurrence, 10)
	if err != nil || len(due) != 1 || due[0].ID != scheduledTrigger.ID {
		t.Fatalf("due triggers=%+v err=%v", due, err)
	}
	earliest, err := repository.EarliestScheduledWorkflowTrigger(ctx, workspaceID)
	if err != nil || !earliest.Equal(occurrence) {
		t.Fatalf("earliest scheduled trigger=%s err=%v", earliest, err)
	}
	nextOccurrence := occurrence.Add(time.Hour)
	advanced, err := repository.CompleteScheduledWorkflowTrigger(ctx, workspaceID, scheduledTrigger.ID, occurrence, nextOccurrence,
		event("fired", "workflow.trigger_fired", occurrence))
	if err != nil || !advanced {
		t.Fatalf("complete scheduled trigger advanced=%v err=%v", advanced, err)
	}
	if replayed, err := repository.CompleteScheduledWorkflowTrigger(ctx, workspaceID, scheduledTrigger.ID, occurrence, nextOccurrence,
		event("fired-replay", "workflow.trigger_fired", occurrence)); err != nil || replayed {
		t.Fatalf("stale schedule completion replayed=%v err=%v", replayed, err)
	}
	if earliest, err := repository.EarliestScheduledWorkflowTrigger(ctx, workspaceID); err != nil || !earliest.Equal(nextOccurrence) {
		t.Fatalf("advanced earliest scheduled trigger=%s err=%v", earliest, err)
	}
	eventTrigger := domain.WorkflowTrigger{
		ID: domain.WorkflowTriggerID("Ft-event-" + suffix), WorkflowID: workflowID, WorkspaceID: workspaceID,
		AppID: workflow.AppID, Title: "On message", Type: "message",
		Config: `{"channel_ids":["` + string(conversationID) + `"]}`, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.SetWorkflowTrigger(ctx, eventTrigger, event("event-trigger", "workflow.trigger_created", now)); err != nil {
		t.Fatal(err)
	}
	eventTriggers, err := repository.ListWorkflowEventTriggers(ctx, workspaceID)
	if err != nil || len(eventTriggers) != 1 || eventTriggers[0].ID != eventTrigger.ID {
		t.Fatalf("event triggers=%+v err=%v", eventTriggers, err)
	}
	if cursor, err := repository.GetWorkflowEventCursor(ctx, workspaceID); err != nil || cursor != 0 {
		t.Fatalf("initial event cursor=%d err=%v", cursor, err)
	}
	if err := repository.AdvanceWorkflowEventCursor(ctx, workspaceID, 41); err != nil {
		t.Fatal(err)
	}
	if err := repository.AdvanceWorkflowEventCursor(ctx, workspaceID, 17); err != nil {
		t.Fatal(err)
	}
	if cursor, err := repository.GetWorkflowEventCursor(ctx, workspaceID); err != nil || cursor != 41 {
		t.Fatalf("event cursor after monotonic advance=%d err=%v, want 41", cursor, err)
	}

	// The activity summary counts runs per status and returns the newest runs
	// first: at this point the workflow has a completed run and a cancelled
	// one, and the completed run carries the higher id.
	summary, err := repository.SummarizeWorkflowRuns(ctx, workspaceID, workflowID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Completed != 1 || summary.Cancelled != 1 || summary.Running != 0 || summary.Failed != 0 || summary.Queued != 0 {
		t.Fatalf("workflow summary=%+v", summary)
	}
	if len(summary.RecentRuns) != 2 || summary.RecentRuns[0].ID != runID || summary.RecentRuns[1].ID != cancellableRun.ID {
		t.Fatalf("recent runs=%+v", summary.RecentRuns)
	}
	if bounded, err := repository.SummarizeWorkflowRuns(ctx, workspaceID, workflowID, 1); err != nil || len(bounded.RecentRuns) != 1 ||
		bounded.RecentRuns[0].ID != runID || bounded.Completed != 1 {
		t.Fatalf("bounded summary=%+v err=%v", bounded, err)
	}

	// Deleting removes the workflow and every record derived from it in one
	// transaction: a running execution is cancelled first, then the workflow,
	// its revisions, triggers, runs, steps, and featured entries all go away.
	deletableRun := domain.WorkflowRun{
		ID: domain.WorkflowRunID("Wx-delete-" + suffix), WorkflowID: workflowID, WorkflowVersion: 2,
		TriggerID: triggerID, WorkspaceID: workspaceID, AppID: workflow.AppID, ActorID: userID,
		Status: domain.WorkflowRunRunning, Inputs: `{}`, Outputs: `{}`, CreatedAt: now, UpdatedAt: now,
	}
	deletableStep := domain.WorkflowStep{
		ID: domain.WorkflowStepID("Fx-delete-" + suffix), WorkflowRunID: deletableRun.ID,
		WorkspaceID: workspaceID, AppID: workflow.AppID, UserID: userID, FunctionID: "FnDelete",
		EditID: "triage", Status: domain.WorkflowStepExecuting, Inputs: `{}`, Outputs: `{}`,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateWorkflowRun(ctx, deletableRun, &deletableStep, []events.Event{
		event("delete-run", "workflow.run_started", now),
	}); err != nil {
		t.Fatal(err)
	}
	head, err := repository.GetWorkflow(ctx, workspaceID, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DeleteWorkflow(ctx, workspaceID, workflowID, head.Version+1, event("delete-stale", "workflow.deleted", now)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale workflow delete error=%v, want ErrConflict", err)
	}
	deleted, err := repository.DeleteWorkflow(ctx, workspaceID, workflowID, head.Version, event("delete", "workflow.deleted", now))
	if err != nil || !deleted {
		t.Fatalf("delete changed=%v err=%v", deleted, err)
	}
	if _, err := repository.GetWorkflow(ctx, workspaceID, workflowID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted workflow error=%v, want ErrNotFound", err)
	}
	if _, err := repository.GetWorkflowTrigger(ctx, workspaceID, triggerID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted trigger error=%v, want ErrNotFound", err)
	}
	if _, err := repository.GetWorkflowRun(ctx, workspaceID, deletableRun.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted run error=%v, want ErrNotFound", err)
	}
	if _, err := repository.GetWorkflowStep(ctx, workspaceID, deletableStep.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted step error=%v, want ErrNotFound", err)
	}
	if featured, err := repository.ListFeaturedWorkflows(ctx, workspaceID, []domain.ConversationID{conversationID}); err != nil || len(featured) != 0 {
		t.Fatalf("featured after delete=%+v err=%v, want none", featured, err)
	}
	if _, err := repository.DeleteWorkflow(ctx, workspaceID, workflowID, head.Version, event("delete-again", "workflow.deleted", now)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("repeat delete error=%v, want ErrNotFound", err)
	}
}

func coreRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspace := domain.Workspace{ID: domain.WorkspaceID("T-qualification-" + suffix), Name: "Qualification"}
	user := domain.User{ID: domain.UserID("U-qualification-" + suffix), WorkspaceID: workspace.ID, Email: "Alice@example.com", Name: "alice"}
	conversation := domain.Conversation{ID: domain.ConversationID("C-qualification-" + suffix), WorkspaceID: workspace.ID, Name: "general"}
	if err := repository.SeedWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	session := domain.SessionRecord{
		WorkspaceID: workspace.ID, UserID: user.ID, Scopes: []string{"openid", "users:read"}, ExpiresAt: time.Now().UTC().Add(time.Hour),
		OIDCProvider: "oidc", OIDCIDToken: "signed.id.token", OIDCSubject: "subject", OIDCSID: "provider-session",
	}
	if err := repository.CreateSession(ctx, "session-"+suffix, session); err != nil {
		t.Fatal(err)
	}
	loadedSession, err := repository.LookupSession(ctx, "session-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if loadedSession.OIDCProvider != session.OIDCProvider || loadedSession.OIDCIDToken != session.OIDCIDToken || loadedSession.OIDCSubject != session.OIDCSubject || loadedSession.OIDCSID != session.OIDCSID {
		t.Fatalf("session metadata=%+v, want=%+v", loadedSession, session)
	}
	if err := repository.SeedConversation(ctx, conversation); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedConversationMember(ctx, conversation.ID, user.ID); err != nil {
		t.Fatal(err)
	}

	loadedUser, err := repository.FindUserByEmail(ctx, workspace.ID, " ALICE@EXAMPLE.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if loadedUser.ID != user.ID || loadedUser.Email != "alice@example.com" {
		t.Fatalf("user=%+v, want normalized email and user identity", loadedUser)
	}

	createdAt := time.Unix(1700000000, 0).UTC()
	message := domain.Message{ID: domain.MessageID("M-qualification-" + suffix), WorkspaceID: workspace.ID, Conversation: conversation.ID, AuthorID: user.ID, Text: "committed", CreatedAt: createdAt}
	event := events.Event{ID: domain.EventID("E-qualification-" + suffix), WorkspaceID: workspace.ID, Topic: "message.created", Payload: string(message.ID), CreatedAt: createdAt}
	idempotencyKey := "idempotency-qualification-" + suffix
	if err := repository.CreateMessage(ctx, message, event, idempotencyKey); err != nil {
		t.Fatal(err)
	}
	duplicate := message
	duplicate.ID = domain.MessageID("M-qualification-duplicate-" + suffix)
	duplicate.Text = "different"
	duplicateEvent := event
	duplicateEvent.ID = domain.EventID("E-qualification-duplicate-" + suffix)
	duplicateEvent.Payload = string(duplicate.ID)
	if err := repository.CreateMessage(ctx, duplicate, duplicateEvent, idempotencyKey); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("duplicate idempotency error=%v, want ErrIdempotencyConflict", err)
	}

	loadedMessage, err := repository.GetMessage(ctx, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedMessage.Text != message.Text || loadedMessage.AuthorID != message.AuthorID {
		t.Fatalf("message=%+v, want committed message", loadedMessage)
	}
	page, err := repository.ListMessages(ctx, conversation.ID, domain.PageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID != message.ID || page.HasMore {
		t.Fatalf("message page=%+v, want one bounded item", page)
	}
	if _, err := repository.GetIdempotentMessage(ctx, workspace.ID, user.ID, idempotencyKey); err != nil {
		t.Fatal(err)
	}
}

func openIDRefreshTokenRotationIsDurable(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-openid-" + suffix)
	userID := domain.UserID("U-openid-" + suffix)
	if err := repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "OpenID qualification"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.SeedUser(ctx, domain.User{ID: userID, WorkspaceID: workspaceID, Email: "openid@example.com", Name: "openid"}); err != nil {
		t.Fatal(err)
	}
	clientID := "openid-client-" + suffix
	if err := repository.CreateOAuthClient(ctx, domain.OAuthClient{ID: clientID, SecretHash: domain.HashToken("secret"), AppID: "A-openid"}); err != nil {
		t.Fatal(err)
	}
	oldRefreshToken := "old-refresh-" + suffix
	newRefreshToken := "new-refresh-" + suffix
	if err := repository.CreateOpenIDRefreshToken(ctx, domain.OpenIDRefreshToken{TokenHash: domain.HashToken(oldRefreshToken), ClientID: clientID, WorkspaceID: workspaceID, UserID: userID, Scopes: []string{"openid"}, ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	rotated, err := repository.ExchangeOpenIDRefreshToken(ctx, clientID, oldRefreshToken, "access-"+suffix, newRefreshToken, domain.OpenIDToken{})
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AccessToken != "access-"+suffix || rotated.RefreshToken != newRefreshToken || rotated.WorkspaceID != workspaceID || rotated.UserID != userID || len(rotated.Scopes) != 1 || rotated.Scopes[0] != "openid" {
		t.Fatalf("rotated token=%+v", rotated)
	}
	if _, err := repository.ExchangeOpenIDRefreshToken(ctx, clientID, oldRefreshToken, "replay-access-"+suffix, "replay-refresh-"+suffix, domain.OpenIDToken{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("replayed refresh token error=%v, want %v", err, store.ErrNotFound)
	}
}

func listsRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, closeRepository := open(t, ctx)
	defer closeRepository()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-lists-" + suffix)
	userID := domain.UserID("U-lists-" + suffix)
	conversationID := domain.ConversationID("C-lists-" + suffix)
	now := time.Unix(1700000000, 0).UTC()
	event := func(id, topic, payload string) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspaceID, Topic: topic, Payload: payload, CreatedAt: now}
	}
	for _, seed := range []func() error{
		func() error { return repository.SeedWorkspace(ctx, domain.Workspace{ID: workspaceID, Name: "Lists"}) },
		func() error {
			return repository.SeedUser(ctx, domain.User{ID: userID, WorkspaceID: workspaceID, Name: "lists-user"})
		},
		func() error {
			return repository.SeedConversation(ctx, domain.Conversation{ID: conversationID, WorkspaceID: workspaceID, Name: "lists-channel"})
		},
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}
	list := domain.List{ID: domain.ListID("F-lists-" + suffix), WorkspaceID: workspaceID, OwnerID: userID, Name: "Lists", DescriptionBlocks: "[]", Schema: `[{"key":"title"}]`, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateList(ctx, list, event("list-create", "list.created", string(list.ID))); err != nil {
		t.Fatal(err)
	}
	item := domain.ListItem{ID: domain.ListItemID("Rec-lists-" + suffix), ListID: list.ID, WorkspaceID: workspaceID, Fields: `[{"column_id":"title","value":"before"}]`, CreatedBy: userID, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateListItem(ctx, item, event("item-create", "list.item.created", string(item.ID))); err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListItems(ctx, workspaceID, list.ID, domain.PageRequest{Limit: 1}, false)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != item.ID || page.HasMore {
		t.Fatalf("list items=%+v err=%v", page, err)
	}
	item.Fields = `[{"column_id":"title","value":"after"}]`
	item.UpdatedAt = now.Add(time.Minute)
	item.UpdatedBy = userID
	item.Version = page.Items[0].Version + 1
	if err := repository.UpdateListItem(ctx, item, event("item-update", "list.item.updated", string(item.ID))); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.GetListItem(ctx, workspaceID, list.ID, item.ID)
	if err != nil || !strings.Contains(loaded.Fields, "after") {
		t.Fatalf("loaded item=%+v err=%v", loaded, err)
	}
	if err := repository.SetListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: domain.GrantChannel, EntityID: string(conversationID), Access: domain.AccessRead}, event("access-set", "list.access.set", string(list.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteListAccess(ctx, domain.ListAccess{ListID: list.ID, EntityType: domain.GrantChannel, EntityID: string(conversationID)}, event("access-delete", "list.access.deleted", string(list.ID))); err != nil {
		t.Fatal(err)
	}
	download := domain.ListDownload{ID: domain.ListDownloadID("export_" + suffix), ListID: list.ID, WorkspaceID: workspaceID, Status: "COMPLETED", URL: "https://example.invalid/export", IncludeArchived: true, CreatedAt: now}
	if err := repository.CreateListDownload(ctx, download, event("download", "list.download.started", string(download.ID))); err != nil {
		t.Fatal(err)
	}
	if loadedDownload, err := repository.GetListDownload(ctx, workspaceID, download.ID); err != nil || loadedDownload.URL != download.URL || !loadedDownload.IncludeArchived {
		t.Fatalf("download=%+v err=%v", loadedDownload, err)
	}
	if err := repository.DeleteListItem(ctx, workspaceID, list.ID, item.ID, event("item-delete", "list.items.deleted", string(item.ID))); err != nil {
		t.Fatal(err)
	}
}

func publishedWaveOneRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-wave-one-" + suffix)
	userID := domain.UserID("U-wave-one-" + suffix)
	conversationID := domain.ConversationID("C-wave-one-" + suffix)
	now := time.Unix(1700000000, 0).UTC()
	event := func(id, topic, payload string) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspaceID, Topic: topic, Payload: payload, CreatedAt: now}
	}
	workspace := domain.Workspace{ID: workspaceID, Name: "Wave one"}
	user := domain.User{ID: userID, WorkspaceID: workspaceID, Email: "wave-one@example.com", Name: "wave-one"}
	conversation := domain.Conversation{ID: conversationID, WorkspaceID: workspaceID, Name: "wave-one"}
	for _, seed := range []func() error{
		func() error { return repository.SeedWorkspace(ctx, workspace) },
		func() error { return repository.SeedUser(ctx, user) },
		func() error { return repository.SeedConversation(ctx, conversation) },
		func() error { return repository.SeedConversationMember(ctx, conversationID, userID) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}

	message := domain.Message{ID: domain.MessageID("M-wave-one-" + suffix), WorkspaceID: workspaceID, Conversation: conversationID, AuthorID: userID, Text: "durable wave one search", CreatedAt: now}
	if err := repository.CreateMessage(ctx, message, event("message", "message.created", string(message.ID)), ""); err != nil {
		t.Fatal(err)
	}
	search, err := repository.SearchMessages(ctx, workspaceID, userID, domain.MessageSearch{
		Terms: []string{"wave one search"}, Page: domain.PageRequest{Limit: 1},
	})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ID != message.ID || search.HasMore {
		t.Fatalf("search=%+v err=%v", search, err)
	}

	presence, err := repository.SetUserPresence(ctx, workspaceID, userID, domain.PresenceAway, event("presence", "user.presence_changed", string(userID)))
	if err != nil || presence.Presence != domain.PresenceAway {
		t.Fatalf("presence=%+v err=%v", presence, err)
	}
	statusDue := now.Add(2 * time.Hour).Truncate(time.Second)
	statusProfile := presence.Profile
	statusProfile.StatusText = "Qualifying persistence"
	statusProfile.StatusEmoji = ":test_tube:"
	statusProfile.StatusExpiration = statusDue
	if _, err := repository.UpdateUserProfile(ctx, workspaceID, userID, statusProfile, event("status-set", "user.profile_changed", string(userID))); err != nil {
		t.Fatal(err)
	}
	dueStatuses, err := repository.DueUserStatuses(ctx, workspaceID, statusDue, 10)
	if err != nil || len(dueStatuses) != 1 || !dueStatuses[0].Profile.StatusExpiration.Equal(statusDue) {
		t.Fatalf("due statuses=%+v err=%v", dueStatuses, err)
	}
	if changed, err := repository.ExpireUserStatus(ctx, workspaceID, userID, statusDue, "", statusDue, event("status-expired", "user.profile_changed", string(userID))); err != nil || !changed {
		t.Fatalf("expire status changed=%t err=%v", changed, err)
	}
	scheduledStart := statusDue.Add(time.Hour)
	scheduledStatus := domain.ScheduledStatus{
		ID: domain.ScheduledStatusID("scheduled-status-" + suffix), WorkspaceID: workspaceID, UserID: userID,
		StatusText: "Scheduled persistence", StatusEmoji: ":calendar:", StartsAt: scheduledStart,
		EndsAt: scheduledStart.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := repository.CreateScheduledStatus(ctx, scheduledStatus); err != nil {
		t.Fatal(err)
	}
	listedStatuses, err := repository.ListScheduledStatuses(ctx, workspaceID, userID)
	if err != nil || len(listedStatuses) != 1 || listedStatuses[0].ID != scheduledStatus.ID {
		t.Fatalf("scheduled statuses=%+v err=%v", listedStatuses, err)
	}
	if changed, err := repository.ActivateScheduledStatus(ctx, workspaceID, userID, scheduledStatus.ID, scheduledStatus.UpdatedAt, scheduledStart, event("status-started", "user.profile_changed", string(userID))); err != nil || !changed {
		t.Fatalf("activate scheduled status changed=%t err=%v", changed, err)
	}
	dueStatuses, err = repository.DueUserStatuses(ctx, workspaceID, scheduledStatus.EndsAt, 10)
	if err != nil || len(dueStatuses) != 1 || dueStatuses[0].Profile.ActiveScheduledStatusID != scheduledStatus.ID {
		t.Fatalf("activated scheduled status=%+v err=%v", dueStatuses, err)
	}
	if changed, err := repository.ExpireUserStatus(ctx, workspaceID, userID, scheduledStatus.EndsAt, scheduledStatus.ID, scheduledStatus.EndsAt, event("scheduled-status-expired", "user.profile_changed", string(userID))); err != nil || !changed {
		t.Fatalf("expire scheduled status changed=%t err=%v", changed, err)
	}
	dnd := domain.DoNotDisturb{WorkspaceID: workspaceID, UserID: userID, Enabled: true, SnoozeUntil: now.Add(time.Hour)}
	if err := repository.SetDoNotDisturb(ctx, dnd, event("dnd", "user.dnd_changed", string(userID))); err != nil {
		t.Fatal(err)
	}
	storedDND, err := repository.GetDoNotDisturb(ctx, workspaceID, userID)
	if err != nil || !storedDND.Enabled || !storedDND.SnoozeUntil.Equal(dnd.SnoozeUntil) || !storedDND.NextStartAt.IsZero() || !storedDND.NextEndAt.IsZero() {
		t.Fatalf("dnd=%+v err=%v", storedDND, err)
	}

	star := domain.Star{Message: message, Conversation: conversationID, UserID: userID, CreatedAt: now}
	if err := repository.AddStar(ctx, star, event("star", "star.added", string(message.ID))); err != nil {
		t.Fatal(err)
	}
	stars, nextStar, moreStars, err := repository.ListStars(ctx, workspaceID, userID, domain.PageRequest{Limit: 1})
	if err != nil || len(stars) != 1 || stars[0].Message.ID != message.ID || nextStar != "" || moreStars {
		t.Fatalf("stars=%+v next=%q more=%v err=%v", stars, nextStar, moreStars, err)
	}
	if err := repository.RemoveStar(ctx, star, event("star-remove", "star.removed", string(message.ID))); err != nil {
		t.Fatal(err)
	}
	stars, _, _, err = repository.ListStars(ctx, workspaceID, userID, domain.PageRequest{Limit: 1})
	if err != nil || len(stars) != 0 {
		t.Fatalf("stars after remove=%+v err=%v", stars, err)
	}

	savedItem := domain.SavedItem{
		ID: domain.SavedItemID("saved-wave-one-" + suffix), WorkspaceID: workspaceID, UserID: userID,
		MessageID: message.ID, Conversation: conversationID, State: domain.SavedItemInProgress,
		CreatedAt: now, UpdatedAt: now, Message: message, SourceAvailable: true,
	}
	createdSaved, created, err := repository.CreateSavedItem(ctx, savedItem, event("saved", "saved_item.created", string(savedItem.ID)))
	if err != nil || !created || createdSaved.ID != savedItem.ID || createdSaved.SourceAvailable || createdSaved.Message.ID != "" {
		t.Fatalf("create saved item=%+v created=%v err=%v", createdSaved, created, err)
	}
	duplicateSaved, created, err := repository.CreateSavedItem(ctx, savedItem, event("saved-duplicate", "saved_item.created", string(savedItem.ID)))
	if err != nil || created || duplicateSaved.ID != savedItem.ID {
		t.Fatalf("idempotent saved item=%+v created=%v err=%v", duplicateSaved, created, err)
	}
	savedPage, err := repository.ListSavedItems(ctx, workspaceID, userID, domain.SavedItemInProgress, domain.PageRequest{Limit: 1})
	if err != nil || len(savedPage.Items) != 1 || savedPage.Items[0].ID != savedItem.ID || savedPage.HasMore {
		t.Fatalf("saved page=%+v err=%v", savedPage, err)
	}
	savedByMessage, err := repository.GetSavedItemByMessage(ctx, workspaceID, userID, message.ID)
	if err != nil || savedByMessage.ID != savedItem.ID {
		t.Fatalf("saved by message=%+v err=%v", savedByMessage, err)
	}
	savedBatch, err := repository.ListSavedItemsForMessages(ctx, workspaceID, userID, []domain.MessageID{message.ID})
	if err != nil || len(savedBatch) != 1 || savedBatch[0].ID != savedItem.ID {
		t.Fatalf("saved batch=%+v err=%v", savedBatch, err)
	}
	savedItem.State = domain.SavedItemCompleted
	savedItem.UpdatedAt = now.Add(time.Minute)
	updatedSaved, err := repository.UpdateSavedItem(ctx, savedItem, event("saved-update", "saved_item.changed", string(savedItem.ID)))
	if err != nil || updatedSaved.State != domain.SavedItemCompleted {
		t.Fatalf("updated saved item=%+v err=%v", updatedSaved, err)
	}
	if err := repository.DeleteSavedItem(ctx, workspaceID, userID, savedItem.ID, event("saved-delete", "saved_item.removed", string(savedItem.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetSavedItem(ctx, workspaceID, userID, savedItem.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted saved item err=%v", err)
	}

	bookmark := domain.Bookmark{ID: domain.BookmarkID("B-wave-one-" + suffix), WorkspaceID: workspaceID, Conversation: conversationID, Title: "Project link", Type: "link", Link: "https://example.com/project", CreatedAt: now, UpdatedAt: now, UpdatedBy: userID}
	if err := repository.CreateBookmark(ctx, bookmark, event("bookmark", "bookmark.created", string(bookmark.ID))); err != nil {
		t.Fatal(err)
	}
	bookmarks, err := repository.ListBookmarks(ctx, workspaceID, conversationID)
	if err != nil || len(bookmarks) != 1 || bookmarks[0].ID != bookmark.ID {
		t.Fatalf("bookmarks=%+v err=%v", bookmarks, err)
	}
	bookmark.Title = "Updated project link"
	bookmark.UpdatedAt = now.Add(time.Minute)
	updatedBookmark, err := repository.UpdateBookmark(ctx, bookmark, event("bookmark-update", "bookmark.updated", string(bookmark.ID)))
	if err != nil || updatedBookmark.Title != bookmark.Title {
		t.Fatalf("updated bookmark=%+v err=%v", updatedBookmark, err)
	}
	if err := repository.DeleteBookmark(ctx, workspaceID, conversationID, bookmark.ID, event("bookmark-delete", "bookmark.removed", string(bookmark.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetBookmark(ctx, workspaceID, conversationID, bookmark.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted bookmark err=%v", err)
	}

	canvas := domain.Canvas{ID: domain.CanvasID("F-wave-one-" + suffix), WorkspaceID: workspaceID, OwnerID: userID, Title: "Qualification canvas", DocumentContent: `{"sections":[{"id":"section-1","type":"h1","text":"Durable canvas"}]}`, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateCanvas(ctx, canvas, event("canvas", "canvas.created", string(canvas.ID))); err != nil {
		t.Fatal(err)
	}
	storedCanvas, err := repository.GetCanvas(ctx, workspaceID, canvas.ID)
	if err != nil || storedCanvas.DocumentContent != canvas.DocumentContent {
		t.Fatalf("canvas=%+v err=%v", storedCanvas, err)
	}
	canvas.Title = "Updated qualification canvas"
	canvas.UpdatedAt = now.Add(time.Minute)
	canvas.Version = storedCanvas.Version + 1
	if err := repository.UpdateCanvas(ctx, canvas, event("canvas-update", "canvas.updated", string(canvas.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: domain.GrantUser, EntityID: string(userID), Access: domain.AccessWrite}, event("canvas-access", "canvas.access_set", string(canvas.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteCanvasAccess(ctx, domain.CanvasAccess{CanvasID: canvas.ID, EntityType: domain.GrantUser, EntityID: string(userID)}, event("canvas-access-delete", "canvas.access_deleted", string(canvas.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.DeleteCanvas(ctx, workspaceID, canvas.ID, event("canvas-delete", "canvas.deleted", string(canvas.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetCanvas(ctx, workspaceID, canvas.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted canvas err=%v", err)
	}

	file := domain.File{ID: domain.FileID("F-wave-one-" + suffix), WorkspaceID: workspaceID, Uploader: userID, Name: "notes.txt", Title: "Notes", MIMEType: "text/plain", BlobKey: string(workspaceID) + "/notes", Size: 7, CreatedAt: now}
	if err := repository.CreateFile(ctx, file, event("file", "file.created", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	files, err := repository.ListFiles(ctx, workspaceID, domain.PageRequest{Limit: 1})
	if err != nil || len(files.Files) != 1 || files.Files[0].BlobKey != file.BlobKey || files.HasMore {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if err := repository.DeleteFile(ctx, file.ID, event("file-delete", "file.deleted", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetFile(ctx, file.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted file err=%v", err)
	}

	remote := domain.RemoteFile{ID: domain.FileID("RF-wave-one-" + suffix), WorkspaceID: workspaceID, ExternalID: "external-" + suffix, Title: "Remote", FileType: "document", ExternalURL: "https://files.example/" + suffix, CreatedAt: now}
	if err := repository.AddRemoteFile(ctx, remote, event("remote", "remote_file.created", string(remote.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetRemoteFileShares(ctx, workspaceID, domain.RemoteFileLookup{ID: remote.ID}, []domain.ConversationID{conversationID}, event("remote-share", "remote_file.shared", string(remote.ID))); err != nil {
		t.Fatal(err)
	}
	remotePage, err := repository.ListRemoteFiles(ctx, workspaceID, domain.PageRequest{Limit: 1})
	if err != nil || len(remotePage.Files) != 1 || len(remotePage.Files[0].SharedChannels) != 1 {
		t.Fatalf("remote files=%+v err=%v", remotePage, err)
	}
	remote.Title = "Updated remote"
	updatedRemote, err := repository.UpdateRemoteFile(ctx, workspaceID, remote, event("remote-update", "remote_file.updated", string(remote.ID)))
	if err != nil || updatedRemote.Title != remote.Title {
		t.Fatalf("updated remote=%+v err=%v", updatedRemote, err)
	}
	if err := repository.RemoveRemoteFile(ctx, workspaceID, domain.RemoteFileLookup{ID: remote.ID}, event("remote-remove", "remote_file.removed", string(remote.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetRemoteFile(ctx, workspaceID, domain.RemoteFileLookup{ID: remote.ID}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed remote file err=%v", err)
	}

	reminder := domain.Reminder{WorkspaceID: workspaceID, ID: domain.ReminderID("R-wave-one-" + suffix), Creator: userID, User: userID, Text: "review wave one", Time: now.Add(time.Hour)}
	if err := repository.CreateReminder(ctx, reminder, event("reminder", "reminder.created", string(reminder.ID))); err != nil {
		t.Fatal(err)
	}
	reminders, err := repository.ListReminders(ctx, workspaceID, userID, domain.PageRequest{Limit: 1})
	if err != nil || len(reminders.Reminders) != 1 || reminders.Reminders[0].ID != reminder.ID || reminders.HasMore {
		t.Fatalf("reminders=%+v err=%v", reminders, err)
	}
	if err := repository.CompleteReminder(ctx, workspaceID, userID, reminder.ID, now.Add(2*time.Hour), event("reminder-complete", "reminder.completed", string(reminder.ID))); err != nil {
		t.Fatal(err)
	}
	completed, err := repository.GetReminder(ctx, workspaceID, userID, reminder.ID)
	if err != nil || completed.CompleteAt.IsZero() {
		t.Fatalf("completed reminder=%+v err=%v", completed, err)
	}
	if err := repository.DeleteReminder(ctx, workspaceID, userID, reminder.ID, event("reminder-delete", "reminder.deleted", string(reminder.ID))); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GetReminder(ctx, workspaceID, userID, reminder.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted reminder err=%v", err)
	}

	scheduled := domain.ScheduledMessage{WorkspaceID: workspaceID, ID: domain.ScheduledMessageID("Q-wave-one-" + suffix), Channel: conversationID, Author: userID, Text: "scheduled wave one", PostAt: now.Add(-time.Minute), CreatedAt: now}
	if err := repository.CreateScheduledMessage(ctx, scheduled, event("scheduled", "message.scheduled", string(scheduled.ID))); err != nil {
		t.Fatal(err)
	}
	scheduledPage, err := repository.ListScheduledMessages(ctx, workspaceID, userID, conversationID, domain.PageRequest{Limit: 1})
	if err != nil || len(scheduledPage.Items) != 1 || scheduledPage.Items[0].ID != scheduled.ID || scheduledPage.HasMore {
		t.Fatalf("scheduled=%+v err=%v", scheduledPage, err)
	}
	claimed, err := repository.ClaimScheduledMessages(ctx, workspaceID, "worker-"+suffix, 1, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].ID != scheduled.ID {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	if err := repository.MarkScheduledMessageDelivered(ctx, "worker-"+suffix, scheduled.ID); err != nil {
		t.Fatal(err)
	}
	scheduledPage, err = repository.ListScheduledMessages(ctx, workspaceID, userID, conversationID, domain.PageRequest{Limit: 1})
	if err != nil || len(scheduledPage.Items) != 0 {
		t.Fatalf("delivered scheduled=%+v err=%v", scheduledPage, err)
	}

	updatedWorkspace, err := repository.SetWorkspaceName(ctx, workspaceID, "Wave one renamed", event("workspace-name", "team.name_changed", string(workspaceID)))
	if err != nil || updatedWorkspace.Name != "Wave one renamed" {
		t.Fatalf("renamed workspace=%+v err=%v", updatedWorkspace, err)
	}
	updatedWorkspace, err = repository.SetWorkspaceDescription(ctx, workspaceID, "Durable storage qualification", event("workspace-description", "team.description_changed", string(workspaceID)))
	if err != nil || updatedWorkspace.Description != "Durable storage qualification" {
		t.Fatalf("described workspace=%+v err=%v", updatedWorkspace, err)
	}
	updatedWorkspace, err = repository.SetWorkspaceDiscoverability(ctx, workspaceID, domain.WorkspaceDiscoverabilityInviteOnly, event("workspace-discoverability", "team.discoverability_changed", string(workspaceID)))
	if err != nil || updatedWorkspace.Discoverability != domain.WorkspaceDiscoverabilityInviteOnly {
		t.Fatalf("discoverable workspace=%+v err=%v", updatedWorkspace, err)
	}
	updatedWorkspace, err = repository.SetWorkspaceIcon(ctx, workspaceID, "https://files.example/icon.png", event("workspace-icon", "team.icon_changed", string(workspaceID)))
	if err != nil || updatedWorkspace.IconURL == "" {
		t.Fatalf("icon workspace=%+v err=%v", updatedWorkspace, err)
	}
	updatedWorkspace, err = repository.SetWorkspaceDefaultChannels(ctx, workspaceID, []domain.ConversationID{conversationID}, event("workspace-defaults", "team.default_channels_changed", string(conversationID)))
	if err != nil || len(updatedWorkspace.DefaultChannelIDs) != 1 || updatedWorkspace.DefaultChannelIDs[0] != conversationID {
		t.Fatalf("default channels=%+v err=%v", updatedWorkspace, err)
	}
	storedWorkspace, err := repository.GetWorkspace(ctx, workspaceID)
	if err != nil || storedWorkspace.Name != updatedWorkspace.Name || len(storedWorkspace.DefaultChannelIDs) != 1 {
		t.Fatalf("stored workspace=%+v err=%v", storedWorkspace, err)
	}

	group := domain.UserGroup{ID: domain.UserGroupID("S-wave-one-" + suffix), WorkspaceID: workspaceID, Name: "Wave one group", Handle: "wave_one", Description: "Qualification group", Creator: userID, UpdatedBy: userID, CreatedAt: now, UpdatedAt: now, Enabled: true}
	if err := repository.CreateUserGroup(ctx, group, event("user-group", "usergroup.created", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserGroupUsers(ctx, workspaceID, group.ID, []domain.UserID{userID}, userID, event("user-group-users", "usergroup.users_changed", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserGroupChannels(ctx, workspaceID, group.ID, []domain.ConversationID{conversationID}, userID, event("user-group-channels", "usergroup.channels_changed", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	groupValue, err := repository.GetUserGroup(ctx, workspaceID, group.ID)
	if err != nil || len(groupValue.Users) != 1 || groupValue.Users[0] != userID || len(groupValue.Channels) != 1 || groupValue.Channels[0] != conversationID {
		t.Fatalf("user group=%+v err=%v", groupValue, err)
	}
	group.Name = "Updated wave one group"
	group.Handle = "updated_wave_one"
	group.UpdatedBy = userID
	group.UpdatedAt = now.Add(time.Minute)
	if err := repository.UpdateUserGroup(ctx, group, event("user-group-update", "usergroup.updated", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetUserGroupEnabled(ctx, workspaceID, group.ID, false, userID, event("user-group-disable", "usergroup.disabled", string(group.ID))); err != nil {
		t.Fatal(err)
	}
	activeGroups, err := repository.ListUserGroups(ctx, workspaceID, false, domain.PageRequest{Limit: 1})
	if err != nil || len(activeGroups.Groups) != 0 {
		t.Fatalf("active groups=%+v err=%v", activeGroups, err)
	}
	allGroups, err := repository.ListUserGroups(ctx, workspaceID, true, domain.PageRequest{Limit: 1})
	if err != nil || len(allGroups.Groups) != 1 || allGroups.Groups[0].Name != group.Name {
		t.Fatalf("all groups=%+v err=%v", allGroups, err)
	}
	if err := repository.SetUserGroupEnabled(ctx, workspaceID, group.ID, true, userID, event("user-group-enable", "usergroup.enabled", string(group.ID))); err != nil {
		t.Fatal(err)
	}

	emoji := domain.CustomEmoji{WorkspaceID: workspaceID, Name: "wave_one", URL: "https://files.example/wave.png"}
	if err := repository.AddEmoji(ctx, emoji, event("emoji-add", "emoji.added", emoji.Name)); err != nil {
		t.Fatal(err)
	}
	emojis, err := repository.ListEmojis(ctx, workspaceID)
	if err != nil || len(emojis) != 1 || emojis[0].Name != emoji.Name {
		t.Fatalf("emojis=%+v err=%v", emojis, err)
	}
	if err := repository.RenameEmoji(ctx, workspaceID, emoji.Name, "wave_updated", event("emoji-rename", "emoji.renamed", emoji.Name)); err != nil {
		t.Fatal(err)
	}
	if err := repository.RemoveEmoji(ctx, workspaceID, "wave_updated", event("emoji-remove", "emoji.removed", "wave_updated")); err != nil {
		t.Fatal(err)
	}
	emojis, err = repository.ListEmojis(ctx, workspaceID)
	if err != nil || len(emojis) != 0 {
		t.Fatalf("emojis after remove=%+v err=%v", emojis, err)
	}
}

func publishedIntegrationRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-integration-" + suffix)
	userID := domain.UserID("U-integration-" + suffix)
	conversationID := domain.ConversationID("C-integration-" + suffix)
	now := time.Unix(1700000000, 0).UTC()
	event := func(id, topic, payload string) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspaceID, Topic: topic, Payload: payload, CreatedAt: now}
	}
	workspace := domain.Workspace{ID: workspaceID, Name: "Integration qualification"}
	user := domain.User{ID: userID, WorkspaceID: workspaceID, Email: "integration@example.com", Name: "integration"}
	conversation := domain.Conversation{ID: conversationID, WorkspaceID: workspaceID, Name: "integration"}
	for _, seed := range []func() error{
		func() error { return repository.SeedWorkspace(ctx, workspace) },
		func() error { return repository.SeedUser(ctx, user) },
		func() error { return repository.SeedConversation(ctx, conversation) },
		func() error { return repository.SeedConversationMember(ctx, conversationID, userID) },
	} {
		if err := seed(); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("conversation preferences normalize and round trip", func(t *testing.T) {
		want := domain.ConversationPrefs{
			ConversationID: conversationID,
			CanThread: domain.ConversationPreferenceList{
				Types: []domain.ConversationPreferenceType{"admins", "members"},
				Users: []domain.UserID{userID},
			},
			WhoCanPost: domain.ConversationPreferenceList{
				Types: []domain.ConversationPreferenceType{"admins"},
				Users: []domain.UserID{userID},
			},
		}
		stored, err := repository.SetConversationPrefs(ctx, conversationID, want, event("prefs", "conversation.preferences_changed", string(conversationID)))
		if err != nil {
			t.Fatal(err)
		}
		if stored.ConversationID != conversationID || len(stored.CanThread.Types) != 2 || len(stored.WhoCanPost.Users) != 1 {
			t.Fatalf("set preferences=%+v", stored)
		}
		stored, err = repository.GetConversationPrefs(ctx, conversationID)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(stored) != fmt.Sprint(want) {
			t.Fatalf("preferences=%+v, want %+v", stored, want)
		}
	})

	t.Run("OAuth authorization code is normalized and single use", func(t *testing.T) {
		clientID := "client-" + suffix
		if err := repository.CreateOAuthClient(ctx, domain.OAuthClient{ID: clientID, SecretHash: domain.HashToken("secret"), AppID: domain.AppID("A-" + suffix)}); err != nil {
			t.Fatal(err)
		}
		client, err := repository.GetOAuthClient(ctx, clientID)
		if err != nil || client.AppID == "" || client.SecretHash == "" {
			t.Fatalf("client=%+v err=%v", client, err)
		}
		code := "code-" + suffix
		redirect := "https://example.test/oauth/callback"
		if err := repository.CreateOAuthCode(ctx, domain.OAuthCode{Code: code, ClientID: clientID, WorkspaceID: workspaceID, UserID: userID, Scopes: []string{"chat:write", " users:read ", "chat:write"}, RedirectURI: redirect}); err != nil {
			t.Fatal(err)
		}
		token, err := repository.ExchangeOAuthCode(ctx, clientID, "secret", code, redirect, "access-"+suffix, domain.OAuthToken{TokenType: "user"})
		if err != nil {
			t.Fatal(err)
		}
		if token.AppID != client.AppID || token.WorkspaceID != workspaceID || token.UserID != userID || fmt.Sprint(token.Scopes) != "[chat:write users:read]" {
			t.Fatalf("token=%+v", token)
		}
		if _, err := repository.ExchangeOAuthCode(ctx, clientID, "secret", code, redirect, "access-replay-"+suffix, domain.OAuthToken{}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("replayed OAuth code error=%v, want ErrNotFound", err)
		}
	})

	t.Run("views retain ownership and enforce expected hash", func(t *testing.T) {
		viewID := domain.ViewID("V-" + suffix)
		view := domain.View{ID: viewID, AppID: domain.AppID("A-" + suffix), WorkspaceID: workspaceID, UserID: userID, Type: "home", ExternalID: "home-" + suffix, Payload: `{"type":"home","blocks":[]}`, Hash: "hash-1", CreatedAt: now, UpdatedAt: now}
		if err := repository.CreateView(ctx, view, event("view-create", "view.created", string(viewID))); err != nil {
			t.Fatal(err)
		}
		loaded, err := repository.GetView(ctx, workspaceID, viewID)
		if err != nil || loaded.UserID != userID || loaded.Payload != view.Payload {
			t.Fatalf("view=%+v err=%v", loaded, err)
		}
		if published, err := repository.GetPublishedView(ctx, workspaceID, userID, view.AppID); err != nil || published.ID != viewID {
			t.Fatalf("published=%+v err=%v", published, err)
		}
		if _, err := repository.GetPublishedView(ctx, workspaceID, userID, "A-other"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another app read the published view: %v", err)
		}
		if _, err := repository.GetViewByExternalID(ctx, workspaceID, "A-other", view.ExternalID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another app resolved the external id: %v", err)
		}
		wrongOwner := view
		wrongOwner.AppID = "A-other"
		if _, err := repository.UpdateView(ctx, wrongOwner, "", event("view-owner", "view.update_rejected", string(viewID))); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("another app updated the view: %v", err)
		}
		updated := view
		updated.Payload = `{"type":"home","blocks":[{"type":"divider"}]}`
		updated.State = `{"values":{"block":{"action":{"type":"plain_text_input","value":"kept"}}}}`
		updated.Errors = map[string]string{"block": "Try again"}
		updated.Hash = "hash-2"
		updated.UpdatedAt = now.Add(time.Minute)
		if _, err := repository.UpdateView(ctx, updated, "stale-hash", event("view-conflict", "view.update_rejected", string(viewID))); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale view update error=%v, want ErrConflict", err)
		}
		updatedView, err := repository.UpdateView(ctx, updated, view.Hash, event("view-update", "view.updated", string(viewID)))
		if err != nil || updatedView.Hash != updated.Hash || updatedView.Payload != updated.Payload ||
			updatedView.State != updated.State || updatedView.Errors["block"] != "Try again" {
			t.Fatalf("updated view=%+v err=%v", updatedView, err)
		}
		if latest, err := repository.GetLatestView(ctx, workspaceID, userID, view.AppID, "home"); err != nil || latest.Hash != "hash-2" {
			t.Fatalf("latest=%+v err=%v", latest, err)
		}
	})

	t.Run("workflow and dialog records survive restart-shaped reads", func(t *testing.T) {
		step := domain.WorkflowStep{ID: domain.WorkflowStepID("W-" + suffix), WorkspaceID: workspaceID, UserID: userID, EditID: "edit-" + suffix, Status: domain.WorkflowStepConfigured, Inputs: `{"input":1}`, Outputs: `[]`, StepName: "Qualification", ImageURL: "https://example.test/step.png", CreatedAt: now, UpdatedAt: now}
		if err := repository.SetWorkflowStep(ctx, step, event("workflow-configured", "workflow.step_configured", string(step.ID))); err != nil {
			t.Fatal(err)
		}
		step.Status = domain.WorkflowStepCompleted
		step.Outputs = `[{"name":"result"}]`
		step.UpdatedAt = now.Add(time.Minute)
		if err := repository.SetWorkflowStep(ctx, step, event("workflow-completed", "workflow.step_completed", string(step.ID))); err != nil {
			t.Fatal(err)
		}
		loadedStep, err := repository.GetWorkflowStep(ctx, workspaceID, step.ID)
		if err != nil || loadedStep.Status != domain.WorkflowStepCompleted || loadedStep.CreatedAt != now {
			t.Fatalf("workflow=%+v err=%v", loadedStep, err)
		}
		dialog := domain.Dialog{ID: domain.DialogID("D-" + suffix), WorkspaceID: workspaceID, UserID: userID, Payload: `{"callback_id":"qualification"}`, CreatedAt: now}
		if err := repository.CreateDialog(ctx, dialog, event("dialog", "dialog.opened", string(dialog.ID))); err != nil {
			t.Fatal(err)
		}
		loadedDialog, err := repository.GetDialog(ctx, workspaceID, dialog.ID)
		if err != nil || loadedDialog.Payload != dialog.Payload || loadedDialog.UserID != userID {
			t.Fatalf("dialog=%+v err=%v", loadedDialog, err)
		}
	})

	t.Run("invite and app approval state transitions are durable", func(t *testing.T) {
		invite := domain.InviteRequest{ID: domain.InviteRequestID("I-" + suffix), WorkspaceID: workspaceID, Email: "invite@example.com", RequestedBy: userID, ChannelIDs: []domain.ConversationID{conversationID}, CustomMessage: "Welcome", RealName: "Invite User", Resend: true, Restricted: true, GuestExpirationAt: now.Add(24 * time.Hour), Status: domain.InviteRequestPending, CreatedAt: now}
		if err := repository.CreateInviteRequest(ctx, invite, event("invite-create", "invite.requested", string(invite.ID))); err != nil {
			t.Fatal(err)
		}
		page, err := repository.ListInviteRequests(ctx, workspaceID, domain.InviteRequestPending, domain.PageRequest{Limit: 1})
		if err != nil || len(page.Requests) != 1 || page.Requests[0].Email != invite.Email || len(page.Requests[0].ChannelIDs) != 1 {
			t.Fatalf("invites=%+v err=%v", page, err)
		}
		if err := repository.SetInviteRequestStatus(ctx, workspaceID, invite.ID, domain.InviteRequestPending, domain.InviteRequestApproved, now.Add(time.Minute), event("invite-approve", "invite.approved", string(invite.ID))); err != nil {
			t.Fatal(err)
		}
		loadedInvite, err := repository.GetInviteRequest(ctx, workspaceID, invite.ID)
		if err != nil || loadedInvite.Status != domain.InviteRequestApproved || loadedInvite.ReviewedAt.IsZero() {
			t.Fatalf("invite=%+v err=%v", loadedInvite, err)
		}
		appID := domain.AppID("A-approval-" + suffix)
		requestID := domain.AppRequestID("AR-" + suffix)
		if err := repository.SetAppApproval(ctx, workspaceID, appID, requestID, domain.AppApprovalApproved, now, event("app-approve", "app.approved", string(appID))); err != nil {
			t.Fatal(err)
		}
		approvals, err := repository.ListAppApprovals(ctx, workspaceID, domain.AppApprovalApproved, domain.PageRequest{Limit: 1})
		if err != nil || len(approvals.Apps) != 1 || approvals.Apps[0].ID != appID {
			t.Fatalf("approvals=%+v err=%v", approvals, err)
		}
		if err := repository.SetAppApproval(ctx, workspaceID, appID, requestID, domain.AppApprovalRestricted, now.Add(time.Minute), event("app-restrict", "app.restricted", string(appID))); err != nil {
			t.Fatal(err)
		}
		restricted, err := repository.ListAppApprovals(ctx, workspaceID, domain.AppApprovalRestricted, domain.PageRequest{Limit: 1})
		if err != nil || len(restricted.Apps) != 1 || restricted.Apps[0].RequestID != requestID {
			t.Fatalf("restricted approvals=%+v err=%v", restricted, err)
		}
		permission := domain.AppPermissionRequest{ID: requestID, WorkspaceID: workspaceID, RequesterID: userID, TargetUserID: userID, Scopes: []string{"users:read", "chat:write", "users:read"}, TriggerID: "trigger-" + suffix, CreatedAt: now}
		if err := repository.CreateAppPermissionRequest(ctx, permission, event("permission", "app.permission_requested", string(requestID))); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("call participants and lifecycle are durable", func(t *testing.T) {
		call := domain.Call{ID: domain.CallID("CA-" + suffix), WorkspaceID: workspaceID, ExternalUniqueID: "external-" + suffix, ExternalDisplayID: "display-" + suffix, JoinURL: "https://example.test/join", DesktopAppJoinURL: "sameoldchat://join/" + suffix, Title: "Qualification call", CreatedBy: userID, Participants: []domain.UserID{userID}, StartedAt: now}
		if err := repository.CreateCall(ctx, call, event("call-create", "call.created", string(call.ID))); err != nil {
			t.Fatal(err)
		}
		loaded, err := repository.GetCall(ctx, workspaceID, call.ID)
		if err != nil || len(loaded.Participants) != 1 || loaded.Participants[0] != userID || loaded.StartedAt != now {
			t.Fatalf("call=%+v err=%v", loaded, err)
		}
		call.Title = "Updated qualification call"
		call.ExternalDisplayID = "display-updated-" + suffix
		if err := repository.UpdateCall(ctx, call, event("call-update", "call.updated", string(call.ID))); err != nil {
			t.Fatal(err)
		}
		if err := repository.SetCallParticipants(ctx, workspaceID, call.ID, []domain.UserID{userID}, event("call-participants", "call.participants_changed", string(call.ID))); err != nil {
			t.Fatal(err)
		}
		if err := repository.EndCall(ctx, workspaceID, call.ID, 90, event("call-end", "call.ended", string(call.ID))); err != nil {
			t.Fatal(err)
		}
		loaded, err = repository.GetCall(ctx, workspaceID, call.ID)
		if err != nil || loaded.Title != call.Title || loaded.DurationSeconds != 90 || loaded.EndedAt.IsZero() {
			t.Fatalf("ended call=%+v err=%v", loaded, err)
		}
	})

	t.Run("RTM connection is consumed once and expires", func(t *testing.T) {
		connection := domain.RTMConnection{ID: "rtm-" + suffix, WorkspaceID: workspaceID, UserID: userID, ExpiresAt: time.Now().UTC().Add(time.Minute)}
		if err := repository.CreateRTMConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		consumed, err := repository.ConsumeRTMConnection(ctx, connection.ID)
		if err != nil || consumed.ID != connection.ID || !consumed.ExpiresAt.Equal(connection.ExpiresAt.Truncate(time.Nanosecond)) {
			t.Fatalf("connection=%+v err=%v", consumed, err)
		}
		if _, err := repository.ConsumeRTMConnection(ctx, connection.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("replayed RTM connection error=%v, want ErrNotFound", err)
		}
	})

	t.Run("app token and Socket Mode connection are durable and single use", func(t *testing.T) {
		appToken := "xapp-qualification-" + suffix
		appID := domain.AppID("A-qualification-" + suffix)
		if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: appID, WorkspaceID: workspaceID, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		installations, err := repository.ListAppInstallations(ctx, appID)
		if err != nil || len(installations) != 1 || installations[0].WorkspaceID != workspaceID {
			t.Fatalf("app installations=%+v err=%v", installations, err)
		}
		if err := repository.SeedAppToken(ctx, appToken, domain.AppTokenRecord{AppID: appID, Scopes: []string{" connections:write ", "connections:write"}}); err != nil {
			t.Fatal(err)
		}
		token, err := repository.LookupAppToken(ctx, appToken)
		if err != nil || token.AppID != appID || len(token.Scopes) != 1 || token.Scopes[0] != "connections:write" {
			t.Fatalf("app token=%+v err=%v", token, err)
		}
		connection := domain.SocketModeConnection{ID: "socket-" + suffix, AppID: token.AppID, ExpiresAt: time.Now().UTC().Add(time.Minute)}
		if err := repository.CreateSocketModeConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		consumed, err := repository.ConsumeSocketModeConnection(ctx, connection.ID)
		if err != nil || consumed.AppID != connection.AppID || !consumed.ExpiresAt.Equal(connection.ExpiresAt.Truncate(time.Nanosecond)) {
			t.Fatalf("Socket Mode connection=%+v err=%v", consumed, err)
		}
		active, err := repository.CountSocketModeConnections(ctx, connection.AppID)
		if err != nil || active != 1 {
			t.Fatalf("active Socket Mode connections=%d err=%v, want 1", active, err)
		}
		if err := repository.RenewSocketModeConnection(ctx, connection.ID, time.Now().UTC().Add(time.Minute)); err != nil {
			t.Fatalf("renew Socket Mode connection error=%v", err)
		}
		if err := repository.ReleaseSocketModeConnection(ctx, connection.ID); err != nil {
			t.Fatalf("release Socket Mode connection error=%v", err)
		}
		active, err = repository.CountSocketModeConnections(ctx, connection.AppID)
		if err != nil || active != 0 {
			t.Fatalf("active Socket Mode connections after release=%d err=%v, want 0", active, err)
		}
		if _, err := repository.ConsumeSocketModeConnection(ctx, connection.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("replayed Socket Mode connection error=%v, want ErrNotFound", err)
		}
		// A ticket is inactive until it is dialled, so the concurrent-connection
		// limit has to hold at consumption. Taking more tickets than the limit
		// and dialling them all is how an app would otherwise exceed it.
		limitTickets := make([]string, 0, domain.SocketModeConnectionLimit+3)
		for index := 0; index <= domain.SocketModeConnectionLimit+2; index++ {
			ticket := domain.SocketModeConnection{ID: fmt.Sprintf("socket-limit-%s-%d", suffix, index), AppID: token.AppID, ExpiresAt: time.Now().UTC().Add(time.Minute)}
			if err := repository.CreateSocketModeConnection(ctx, ticket); err != nil {
				t.Fatalf("issue Socket Mode ticket %d: %v", index, err)
			}
			limitTickets = append(limitTickets, ticket.ID)
		}
		dialled := 0
		for _, ticket := range limitTickets {
			if _, err := repository.ConsumeSocketModeConnection(ctx, ticket); err == nil {
				dialled++
				continue
			} else if !errors.Is(err, store.ErrSocketModeConnectionLimit) {
				t.Fatalf("dial Socket Mode ticket %s: %v", ticket, err)
			}
		}
		if dialled != domain.SocketModeConnectionLimit {
			t.Fatalf("dialled %d Socket Mode connections, want the limit of %d", dialled, domain.SocketModeConnectionLimit)
		}
		active, err = repository.CountSocketModeConnections(ctx, token.AppID)
		if err != nil || active != domain.SocketModeConnectionLimit {
			t.Fatalf("active Socket Mode connections=%d err=%v, want %d", active, err, domain.SocketModeConnectionLimit)
		}
		for _, ticket := range limitTickets {
			if err := repository.ReleaseSocketModeConnection(ctx, ticket); err != nil && !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("release Socket Mode ticket %s: %v", ticket, err)
			}
		}
		before, err := repository.ListAppEventsAfter(ctx, appID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var after uint64
		if len(before) > 0 {
			after = before[len(before)-1].Sequence
		}
		if err := repository.AppendEvent(ctx, event("socket-event", "message.created", "socket-event")); err != nil {
			t.Fatal(err)
		}
		records, err := repository.ListAppEventsAfter(ctx, appID, after, 10)
		if err != nil || len(records) != 1 || records[0].Event.Topic != "message.created" {
			t.Fatalf("app events=%+v err=%v", records, err)
		}
		if err := repository.SetSocketModeCursor(ctx, appID, records[0].Sequence); err != nil {
			t.Fatal(err)
		}
		cursor, err := repository.GetSocketModeCursor(ctx, appID)
		if err != nil || cursor != records[0].Sequence {
			t.Fatalf("cursor=%d err=%v", cursor, err)
		}
		if err := repository.SetSocketModeCursor(ctx, appID, cursor-1); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("cursor regression error=%v, want ErrConflict", err)
		}
		response := domain.SocketModeResponse{AppID: appID, EnvelopeID: "event-4-" + suffix, Payload: `{"ok":true}`, ReceivedAt: time.Now().UTC()}
		if err := repository.RecordSocketModeResponse(ctx, response); err != nil {
			t.Fatal(err)
		}
		if err := repository.RecordSocketModeResponse(ctx, response); err != nil {
			t.Fatalf("identical response replay error=%v", err)
		}
		conflict := response
		conflict.Payload = `{"ok":false}`
		if err := repository.RecordSocketModeResponse(ctx, conflict); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("conflicting response replay error=%v, want ErrConflict", err)
		}
		queued := domain.SocketModeResponse{AppID: domain.AppID("A-queue-qualification-" + suffix), EnvelopeID: "event-5-" + suffix, Payload: `{"ok":true}`, ReceivedAt: time.Now().UTC()}
		if err := repository.RecordSocketModeResponse(ctx, queued); err != nil {
			t.Fatal(err)
		}
		claimed, err := repository.ClaimSocketModeResponses(ctx, queued.AppID, "qualification-worker", 10, time.Minute)
		if err != nil || len(claimed) != 1 || claimed[0].EnvelopeID != queued.EnvelopeID {
			t.Fatalf("claimed responses=%+v err=%v", claimed, err)
		}
		if err := repository.RenewSocketModeResponses(ctx, "qualification-worker", claimed, time.Minute); err != nil {
			t.Fatalf("renew Socket Mode response error=%v", err)
		}
		if err := repository.AckSocketModeResponses(ctx, "qualification-worker", claimed); err != nil {
			t.Fatal(err)
		}
		if claimed, err := repository.ClaimSocketModeResponses(ctx, queued.AppID, "other-worker", 10, time.Minute); err != nil || len(claimed) != 0 {
			t.Fatalf("acknowledged responses reclaimed=%+v err=%v", claimed, err)
		}
		retry := queued
		retry.EnvelopeID = "event-6-" + suffix
		if err := repository.RecordSocketModeResponse(ctx, retry); err != nil {
			t.Fatal(err)
		}
		claimed, err = repository.ClaimSocketModeResponses(ctx, retry.AppID, "qualification-worker", 10, time.Minute)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("retry response claim=%+v err=%v", claimed, err)
		}
		if err := repository.ReleaseSocketModeResponses(ctx, "qualification-worker", claimed, time.Now().UTC().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		claimed, err = repository.ClaimSocketModeResponses(ctx, retry.AppID, "other-worker", 10, time.Minute)
		if err != nil || len(claimed) != 1 || claimed[0].EnvelopeID != retry.EnvelopeID {
			t.Fatalf("released response claim=%+v err=%v", claimed, err)
		}
	})
}

func durableEventDeliveryRepositoryContract(t *testing.T, open opener) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := open(t, ctx)
	defer closeRepository()

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	workspaceID := domain.WorkspaceID("T-events-" + suffix)
	createdAt := time.Unix(1700000000, 123).UTC()
	appendEvent := func(id, topic string) events.Event {
		return events.Event{ID: domain.EventID(id + "-" + suffix), WorkspaceID: workspaceID, Topic: topic, Payload: id, CreatedAt: createdAt}
	}
	for _, event := range []events.Event{
		appendEvent("message", "message.created"),
		appendEvent("blob", events.FileBlobDeleteTopic),
		appendEvent("presence", "user.presence_changed"),
	} {
		if err := repository.AppendEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := repository.ListEventsAfter(ctx, workspaceID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Event.Topic != "message.created" || listed[1].Event.Topic != "user.presence_changed" {
		t.Fatalf("listed events=%+v, want non-blob events in sequence order", listed)
	}
	if !listed[0].Event.CreatedAt.Equal(createdAt) {
		t.Fatalf("event timestamp=%s, want %s", listed[0].Event.CreatedAt, createdAt)
	}

	claimed, err := repository.ClaimEvents(ctx, workspaceID, "worker-a", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 || claimed[0].Event.ID != listed[0].Event.ID || claimed[1].Event.ID != listed[1].Event.ID {
		t.Fatalf("claimed events=%+v, want both normal events", claimed)
	}
	sequences := []uint64{claimed[0].Sequence, claimed[1].Sequence}
	if next, err := repository.ClaimEvents(ctx, workspaceID, "worker-b", 10, time.Minute); err != nil {
		t.Fatal(err)
	} else if len(next) != 0 {
		t.Fatalf("events claimed by a second worker while leased=%+v", next)
	}
	if err := repository.RenewEvents(ctx, "worker-a", sequences, time.Minute); err != nil {
		t.Fatal(err)
	}
	// The retry instant has to outlast the round trips that follow it, not just
	// the statement that sets it. This budgeted 40ms and then spent it on two
	// transactions against a containerised PostgreSQL on shared CI hardware,
	// which is how it eventually reported that the store had ignored a retry
	// time it had honoured. A second is not a guess at how slow the machine is;
	// it is wide enough that only a stall which would fail this suite many other
	// ways could cross it.
	//
	// The rule itself is already proven without a clock at all, by
	// sqlstore/storedtime_test.go injecting `now`. That seam is unexported, so
	// this cross-profile contract cannot use it, and what it adds is that every
	// profile agrees — not a second measurement of the timing.
	retryAt := time.Now().UTC().Add(time.Second)
	if err := repository.ReleaseEvents(ctx, "worker-a", sequences[:1], retryAt); err != nil {
		t.Fatal(err)
	}
	if next, err := repository.ClaimEvents(ctx, workspaceID, "worker-b", 10, time.Minute); err != nil {
		t.Fatal(err)
	} else if len(next) != 0 {
		t.Fatalf("released event became claimable before retry time=%+v", next)
	}
	// Waiting for the condition rather than sleeping a fixed span: a sleep long
	// enough to be safe is wasted on a fast machine, and one short enough to be
	// quick fails on a slow one. The deadline is generous because exceeding it
	// means the event never became claimable, which is a real defect.
	var retried []events.Record
	for deadline := time.Now().Add(30 * time.Second); ; {
		var err error
		retried, err = repository.ClaimEvents(ctx, workspaceID, "worker-b", 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(retried) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(retried) != 1 || retried[0].Sequence != sequences[0] {
		t.Fatalf("retried events=%+v, want released sequence %d", retried, sequences[0])
	}
	if err := repository.AckEvents(ctx, "worker-b", []uint64{retried[0].Sequence}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckEvents(ctx, "worker-a", []uint64{sequences[1]}); err != nil {
		t.Fatal(err)
	}
	if after, err := repository.ListEventsAfter(ctx, workspaceID, 0, 10); err != nil {
		t.Fatal(err)
	} else if len(after) != 2 {
		t.Fatalf("delivered events disappeared from journal=%+v", after)
	}

	blobs, err := repository.ClaimEventsForTopic(ctx, workspaceID, events.FileBlobDeleteTopic, "blob-worker", 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(blobs) != 1 || blobs[0].Event.Topic != events.FileBlobDeleteTopic {
		t.Fatalf("topic-specific claim=%+v", blobs)
	}
	if err := repository.AckEvents(ctx, "blob-worker", []uint64{blobs[0].Sequence}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AckEvents(ctx, "blob-worker", []uint64{blobs[0].Sequence}); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("repeated acknowledgement error=%v, want ErrLeaseConflict", err)
	}
}
