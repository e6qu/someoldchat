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
}
