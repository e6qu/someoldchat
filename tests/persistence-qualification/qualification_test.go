package qualification

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

type qualificationStore interface {
	store.Store
	SeedAppToken(context.Context, string, domain.AppTokenRecord) error
	SeedWorkspace(context.Context, domain.Workspace) error
	SeedUser(context.Context, domain.User) error
	SeedConversation(context.Context, domain.Conversation) error
	SeedConversationMember(context.Context, domain.ConversationID, domain.UserID) error
}

func TestCoreRepositoryContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := openStore(t, ctx)
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

func TestPublishedWaveOneRepositoryContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := openStore(t, ctx)
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
	search, err := repository.SearchMessages(ctx, workspaceID, userID, "wave one search", domain.PageRequest{Limit: 1})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ID != message.ID || search.HasMore {
		t.Fatalf("search=%+v err=%v", search, err)
	}

	presence, err := repository.SetUserPresence(ctx, workspaceID, userID, domain.PresenceAway, event("presence", "user.presence_changed", string(userID)))
	if err != nil || presence.Presence != domain.PresenceAway {
		t.Fatalf("presence=%+v err=%v", presence, err)
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

func TestPublishedIntegrationRepositoryContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := openStore(t, ctx)
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
		token, err := repository.ExchangeOAuthCode(ctx, clientID, "secret", code, redirect, "access-"+suffix, domain.OAuthToken{TokenType: "bot"})
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
		view := domain.View{ID: viewID, WorkspaceID: workspaceID, UserID: userID, Type: "home", ExternalID: "home-" + suffix, Payload: `{"type":"home","blocks":[]}`, Hash: "hash-1", CreatedAt: now, UpdatedAt: now}
		if err := repository.CreateView(ctx, view, event("view-create", "view.created", string(viewID))); err != nil {
			t.Fatal(err)
		}
		loaded, err := repository.GetView(ctx, workspaceID, viewID)
		if err != nil || loaded.UserID != userID || loaded.Payload != view.Payload {
			t.Fatalf("view=%+v err=%v", loaded, err)
		}
		if published, err := repository.GetPublishedView(ctx, workspaceID, userID); err != nil || published.ID != viewID {
			t.Fatalf("published=%+v err=%v", published, err)
		}
		updated := view
		updated.Payload = `{"type":"home","blocks":[{"type":"divider"}]}`
		updated.Hash = "hash-2"
		updated.UpdatedAt = now.Add(time.Minute)
		if _, err := repository.UpdateView(ctx, updated, "stale-hash", event("view-conflict", "view.update_rejected", string(viewID))); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale view update error=%v, want ErrConflict", err)
		}
		updatedView, err := repository.UpdateView(ctx, updated, view.Hash, event("view-update", "view.updated", string(viewID)))
		if err != nil || updatedView.Hash != updated.Hash || updatedView.Payload != updated.Payload {
			t.Fatalf("updated view=%+v err=%v", updatedView, err)
		}
		if latest, err := repository.GetLatestView(ctx, workspaceID, userID, "home"); err != nil || latest.Hash != "hash-2" {
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
		if err := repository.SetInviteRequestStatus(ctx, workspaceID, invite.ID, domain.InviteRequestApproved, now.Add(time.Minute), event("invite-approve", "invite.approved", string(invite.ID))); err != nil {
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
		if err := repository.CreateAppInstallation(ctx, domain.AppInstallation{AppID: "A-qualification", WorkspaceID: workspaceID, Enabled: true, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		installations, err := repository.ListAppInstallations(ctx, "A-qualification")
		if err != nil || len(installations) != 1 || installations[0].WorkspaceID != workspaceID {
			t.Fatalf("app installations=%+v err=%v", installations, err)
		}
		if err := repository.SeedAppToken(ctx, appToken, domain.AppTokenRecord{AppID: "A-qualification", Scopes: []string{" connections:write ", "connections:write"}}); err != nil {
			t.Fatal(err)
		}
		token, err := repository.LookupAppToken(ctx, appToken)
		if err != nil || token.AppID != "A-qualification" || len(token.Scopes) != 1 || token.Scopes[0] != "connections:write" {
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
		before, err := repository.ListAppEventsAfter(ctx, "A-qualification", 0, 100)
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
		records, err := repository.ListAppEventsAfter(ctx, "A-qualification", after, 10)
		if err != nil || len(records) != 1 || records[0].Event.Topic != "message.created" {
			t.Fatalf("app events=%+v err=%v", records, err)
		}
		if err := repository.SetSocketModeCursor(ctx, "A-qualification", records[0].Sequence); err != nil {
			t.Fatal(err)
		}
		cursor, err := repository.GetSocketModeCursor(ctx, "A-qualification")
		if err != nil || cursor != records[0].Sequence {
			t.Fatalf("cursor=%d err=%v", cursor, err)
		}
		if err := repository.SetSocketModeCursor(ctx, "A-qualification", cursor-1); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("cursor regression error=%v, want ErrConflict", err)
		}
		response := domain.SocketModeResponse{AppID: "A-qualification", EnvelopeID: "event-4", Payload: `{"ok":true}`, ReceivedAt: time.Now().UTC()}
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
		queued := domain.SocketModeResponse{AppID: "A-queue-qualification", EnvelopeID: "event-5", Payload: `{"ok":true}`, ReceivedAt: time.Now().UTC()}
		if err := repository.RecordSocketModeResponse(ctx, queued); err != nil {
			t.Fatal(err)
		}
		claimed, err := repository.ClaimSocketModeResponses(ctx, queued.AppID, "qualification-worker", 10, time.Minute)
		if err != nil || len(claimed) != 1 || claimed[0].EnvelopeID != queued.EnvelopeID {
			t.Fatalf("claimed responses=%+v err=%v", claimed, err)
		}
		if err := repository.AckSocketModeResponses(ctx, "qualification-worker", claimed); err != nil {
			t.Fatal(err)
		}
		if claimed, err := repository.ClaimSocketModeResponses(ctx, queued.AppID, "other-worker", 10, time.Minute); err != nil || len(claimed) != 0 {
			t.Fatalf("acknowledged responses reclaimed=%+v err=%v", claimed, err)
		}
		retry := queued
		retry.EnvelopeID = "event-6"
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

func TestDurableEventDeliveryRepositoryContract(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	repository, closeRepository := openStore(t, ctx)
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
	if err := repository.ReleaseEvents(ctx, "worker-a", sequences[:1], time.Now().UTC().Add(40*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if next, err := repository.ClaimEvents(ctx, workspaceID, "worker-b", 10, time.Minute); err != nil {
		t.Fatal(err)
	} else if len(next) != 0 {
		t.Fatalf("released event became claimable before retry time=%+v", next)
	}
	time.Sleep(60 * time.Millisecond)
	retried, err := repository.ClaimEvents(ctx, workspaceID, "worker-b", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
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
