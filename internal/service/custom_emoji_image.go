package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/thumbnail"
)

// customEmojiImageTypes are the image types Slack accepts for a custom emoji.
var customEmojiImageTypes = map[string]bool{"image/png": true, "image/gif": true, "image/jpeg": true}

// AdminUploadEmoji adds a custom emoji from an uploaded image, as Slack's "Add
// custom emoji" dialog does: a PNG, GIF, or JPEG of at most 128 KB, scaled
// down to fit CustomEmojiSide when it is larger. The image
// is stored as its own blob rather than as a file, so it is not listed among
// anyone's files and is reclaimed when the emoji is removed; its URL needs no
// credentials, as Slack's emoji URLs do not.
func (m Messages) AdminUploadEmoji(ctx context.Context, workspaceID domain.WorkspaceID, userID domain.UserID, name, mimeType string, image []byte) error {
	if err := m.requireWorkspaceAdmin(ctx, workspaceID, userID); err != nil {
		return err
	}
	name = normalizeEmojiName(name)
	if !validEmojiName(name) {
		return domain.ErrInvalidEmoji
	}
	mimeType = normalizeImageContentType(mimeType)
	sniffed := normalizeImageContentType(http.DetectContentType(image))
	if len(image) == 0 || len(image) > domain.MaxCustomEmojiBytes || !customEmojiImageTypes[mimeType] || sniffed != mimeType {
		return domain.ErrInvalidEmojiImage
	}
	// An image larger than an emoji is ever shown is scaled down, as Slack's
	// upload does, keeping its format and, for a GIF, its animation. One that
	// does not decode is refused here rather than stored as a broken emoji.
	image, _, err := thumbnail.Fit(image, mimeType, domain.CustomEmojiSide)
	if err != nil {
		return domain.ErrInvalidEmojiImage
	}
	if nameShadowsBuiltInEmoji(name) {
		return domain.ErrEmojiAlreadyExists
	}
	if m.Blob == nil {
		return domain.ErrBlobUnavailable
	}
	token, err := domain.PublicID("emoji_")
	if err != nil {
		return err
	}
	imageURL := domain.CustomEmojiImageURL(workspaceID, token)
	key, _ := domain.CustomEmojiBlobKey(workspaceID, imageURL)
	if _, err := m.Blob.Put(ctx, key, int64(len(image)), bytes.NewReader(image)); err != nil {
		if errors.Is(err, blob.ErrUnavailable) {
			return domain.ErrBlobUnavailable
		}
		return err
	}
	now := time.Now().UTC()
	event, err := newEvent(workspaceID, userID, events.NewPayload("emoji.added", events.String("name", name), events.String("value", imageURL)), now)
	if err == nil {
		err = m.Store.AddEmoji(ctx, domain.CustomEmoji{WorkspaceID: workspaceID, Name: name, URL: imageURL, CreatedAt: now, CreatedBy: userID}, event)
	}
	if err != nil {
		// Nothing refers to the blob yet, so it is removed here rather than
		// left for a cleanup record that was never written.
		if cleanupErr := m.Blob.Delete(context.Background(), key); cleanupErr != nil && !errors.Is(cleanupErr, blob.ErrNotFound) {
			err = errors.Join(err, fmt.Errorf("blob cleanup: %w", cleanupErr))
		}
		if errors.Is(err, store.ErrAlreadyExists) {
			return domain.ErrEmojiAlreadyExists
		}
		return err
	}
	return nil
}

// OpenEmojiImage reads an uploaded custom emoji's image. Like Slack's emoji
// URLs it needs no credentials: the token is unguessable, and only an image
// a current emoji names is served, so a removed emoji's URL stops working at
// once rather than when the cleanup worker reaches its blob.
func (m Messages) OpenEmojiImage(ctx context.Context, workspaceID domain.WorkspaceID, token string) (string, []byte, error) {
	if m.Blob == nil {
		return "", nil, domain.ErrBlobUnavailable
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.Contains(token, "/") {
		return "", nil, store.ErrNotFound
	}
	emojis, err := m.Store.ListEmojis(ctx, workspaceID)
	if err != nil {
		return "", nil, err
	}
	imageURL, current := domain.CustomEmojiImageURL(workspaceID, token), false
	for _, value := range emojis {
		if value.AliasFor == "" && value.URL == imageURL {
			current = true
			break
		}
	}
	if !current {
		return "", nil, store.ErrNotFound
	}
	key, _ := domain.CustomEmojiBlobKey(workspaceID, imageURL)
	_, reader, err := m.Blob.Open(ctx, key)
	if errors.Is(err, blob.ErrUnavailable) {
		return "", nil, domain.ErrBlobUnavailable
	}
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()
	var image bytes.Buffer
	if _, err := image.ReadFrom(io.LimitReader(reader, domain.MaxCustomEmojiBytes+1)); err != nil {
		return "", nil, err
	}
	if image.Len() > domain.MaxCustomEmojiBytes {
		return "", nil, domain.ErrInvalidEmojiImage
	}
	mimeType := normalizeImageContentType(http.DetectContentType(image.Bytes()))
	if !customEmojiImageTypes[mimeType] {
		return "", nil, domain.ErrInvalidEmojiImage
	}
	return mimeType, image.Bytes(), nil
}
