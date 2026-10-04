package service

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"path"
	"path/filepath"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

// An uploaded custom emoji is stored as its own blob and served from a public
// URL; Slack's rules refuse an image that is too large or not an image, and
// removing the emoji stops its URL at once and records its blob for cleanup in
// the same commit.
func TestAnUploadedCustomEmojiIsServedAndReclaimed(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1"})
	s.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1"})
	if err := s.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	image := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\xff\xff\xff\x00\x00\x00!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;") // a real 1x1 GIF: the upload decodes it

	if err := messages.AdminUploadEmoji(ctx, "T1", "U1", "nope", "image/gif", image); !errors.Is(err, domain.ErrNotWorkspaceAdmin) {
		t.Fatalf("a member uploaded an emoji: %v", err)
	}
	if err := messages.AdminUploadEmoji(ctx, "T1", "UA", "huge", "image/gif", append(append([]byte(nil), image...), make([]byte, domain.MaxCustomEmojiBytes)...)); !errors.Is(err, domain.ErrInvalidEmojiImage) {
		t.Fatalf("an image over 128 KB was accepted: %v", err)
	}
	if err := messages.AdminUploadEmoji(ctx, "T1", "UA", "page", "image/gif", []byte("<html><body>not an image</body></html>")); !errors.Is(err, domain.ErrInvalidEmojiImage) {
		t.Fatalf("a document labelled as an image was accepted: %v", err)
	}
	if err := messages.AdminUploadEmoji(ctx, "T1", "UA", "wavy-cat", "image/gif", image); err != nil {
		t.Fatal(err)
	}
	emojis, err := s.ListEmojis(ctx, "T1")
	if err != nil || len(emojis) != 1 {
		t.Fatalf("emoji=%v err=%v", emojis, err)
	}
	imageURL := emojis[0].URL
	if want := "/emoji/T1/"; len(imageURL) <= len(want) || imageURL[:len(want)] != want {
		t.Fatalf("image URL %q is not one this server serves", imageURL)
	}
	token := path.Base(imageURL)
	mimeType, served, err := messages.OpenEmojiImage(ctx, "T1", token)
	if err != nil || mimeType != "image/gif" || !bytes.Equal(served, image) {
		t.Fatalf("served %q %d bytes err=%v", mimeType, len(served), err)
	}
	if _, _, err := messages.OpenEmojiImage(ctx, "T2", token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another workspace served the image: %v", err)
	}

	if err := messages.AdminRemoveEmoji(ctx, "T1", "UA", "wavy-cat"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := messages.OpenEmojiImage(ctx, "T1", token); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a removed emoji's image was still served: %v", err)
	}
	cleanup := 0
	for _, event := range s.Outbox() {
		if event.Topic == events.CustomEmojiBlobDeleteTopic && event.Payload == "T1/emoji/"+token {
			cleanup++
		}
	}
	if cleanup != 1 {
		t.Fatalf("removal recorded %d cleanups of the image blob, want 1", cleanup)
	}
}

// Slack shrinks an uploaded custom emoji to fit 128 pixels; the stored image
// is the resized one, in the format that was uploaded.
func TestAnUploadedCustomEmojiIsResizedToFit(t *testing.T) {
	ctx := context.Background()
	s := memory.New()
	if err := s.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	s.SeedUser(domain.User{ID: "UA", WorkspaceID: "T1"})
	if err := s.SeedWorkspaceRole("T1", "UA", domain.WorkspaceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	objects, err := blob.NewFilesystem(filepath.Join(t.TempDir(), "objects"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	messages := Messages{Store: s, Blob: objects}
	var upload bytes.Buffer
	if err := png.Encode(&upload, image.NewRGBA(image.Rect(0, 0, 300, 150))); err != nil {
		t.Fatal(err)
	}
	if err := messages.AdminUploadEmoji(ctx, "T1", "UA", "wide", "image/png", upload.Bytes()); err != nil {
		t.Fatal(err)
	}
	emojis, err := s.ListEmojis(ctx, "T1")
	if err != nil || len(emojis) != 1 {
		t.Fatalf("emoji=%v err=%v", emojis, err)
	}
	mimeType, served, err := messages.OpenEmojiImage(ctx, "T1", path.Base(emojis[0].URL))
	if err != nil || mimeType != "image/png" {
		t.Fatalf("served %q err=%v", mimeType, err)
	}
	stored, err := png.DecodeConfig(bytes.NewReader(served))
	if err != nil || stored.Width != domain.CustomEmojiSide || stored.Height != domain.CustomEmojiSide/2 {
		t.Fatalf("stored %dx%d err=%v, want %dx%d", stored.Width, stored.Height, err, domain.CustomEmojiSide, domain.CustomEmojiSide/2)
	}
}
