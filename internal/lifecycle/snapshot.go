package lifecycle

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/blob"
)

type Manifest struct {
	FormatVersion      int    `json:"format_version"`
	ManifestVersion    int    `json:"manifest_version"`
	Generation         uint64 `json:"generation"`
	PreviousGeneration uint64 `json:"previous_generation"`
	Backend            string `json:"backend"`
	SchemaVersion      int    `json:"schema_version"`
	ApplicationVersion string `json:"application_version"`
	MinRestorerVersion string `json:"min_restorer_version"`
	MaxRestorerVersion string `json:"max_restorer_version"`
	CreatedAt          string `json:"created_at"`
	VerifiedAt         string `json:"verified_at"`
	PlaintextSHA256    string `json:"plaintext_sha256"`
	CiphertextSHA256   string `json:"ciphertext_sha256"`
	PlaintextBytes     int64  `json:"plaintext_bytes"`
	CiphertextBytes    int64  `json:"ciphertext_bytes"`
	Artifact           string `json:"artifact"`
	KeyID              string `json:"key_id"`
	Signature          string `json:"signature"`
}

type SnapshotManager struct {
	Root          string
	ObjectStore   blob.ListStore
	EncryptionKey []byte
	SigningKey    []byte
	KeyID         string
	MaxBytes      int64
}

type snapshotIntegrityError struct{ err error }

func (e snapshotIntegrityError) Error() string { return e.err.Error() }

func (e snapshotIntegrityError) Unwrap() error { return e.err }

func invalidSnapshot(message string) error {
	return snapshotIntegrityError{err: errors.New(message)}
}

func isSnapshotIntegrityError(err error) bool {
	var integrityErr snapshotIntegrityError
	return errors.As(err, &integrityErr)
}

func NewSnapshotManager(root string, encryptionKey, signingKey []byte, keyID string, maxBytes int64) (SnapshotManager, error) {
	if strings.TrimSpace(root) == "" || filepath.IsAbs(root) == false {
		return SnapshotManager{}, errors.New("snapshot root must be an absolute path")
	}
	if len(encryptionKey) != 32 || len(signingKey) < 32 {
		return SnapshotManager{}, errors.New("snapshot keys are too short")
	}
	if strings.TrimSpace(keyID) == "" || maxBytes <= 0 {
		return SnapshotManager{}, errors.New("snapshot key ID and positive size limit are required")
	}
	return SnapshotManager{Root: root, EncryptionKey: append([]byte(nil), encryptionKey...), SigningKey: append([]byte(nil), signingKey...), KeyID: keyID, MaxBytes: maxBytes}, nil
}

func NewObjectSnapshotManager(store blob.ListStore, encryptionKey, signingKey []byte, keyID string, maxBytes int64) (SnapshotManager, error) {
	if store == nil {
		return SnapshotManager{}, errors.New("object snapshot manager requires an object store")
	}
	if len(encryptionKey) != 32 || len(signingKey) < 32 {
		return SnapshotManager{}, errors.New("snapshot keys are too short")
	}
	if strings.TrimSpace(keyID) == "" || maxBytes <= 0 {
		return SnapshotManager{}, errors.New("snapshot key ID and positive size limit are required")
	}
	return SnapshotManager{ObjectStore: store, EncryptionKey: append([]byte(nil), encryptionKey...), SigningKey: append([]byte(nil), signingKey...), KeyID: keyID, MaxBytes: maxBytes}, nil
}

func (m SnapshotManager) configured() bool {
	if m.ObjectStore == nil && !filepath.IsAbs(m.Root) {
		return false
	}
	return len(m.EncryptionKey) == 32 && len(m.SigningKey) >= 32 && strings.TrimSpace(m.KeyID) != "" && m.MaxBytes > 0
}

func (m SnapshotManager) Create(ctx context.Context, sourcePath string, metadata Manifest) (returnMetadata Manifest, returnErr error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if metadata.Generation == 0 || metadata.Backend == "" || metadata.SchemaVersion < 1 {
		return Manifest{}, errors.New("snapshot metadata is incomplete")
	}
	if err := m.ensureMonotonicGeneration(ctx, metadata.Generation); err != nil {
		return Manifest{}, err
	}
	previous, previousErr := m.Current(ctx, metadata.Generation)
	if previousErr == nil {
		metadata.PreviousGeneration = previous.Generation
	} else if !errors.Is(previousErr, os.ErrNotExist) && !errors.Is(previousErr, blob.ErrNotFound) {
		return Manifest{}, fmt.Errorf("read previous verified snapshot: %w", previousErr)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, source.Close())
	}()
	artifactName := fmt.Sprintf("artifacts/%020d.bin", metadata.Generation)
	temporaryDirectory := os.TempDir()
	artifactPath := ""
	if m.ObjectStore == nil {
		if err := os.MkdirAll(filepath.Join(m.Root, "artifacts"), 0o700); err != nil {
			return Manifest{}, err
		}
		if err := os.MkdirAll(filepath.Join(m.Root, "manifests"), 0o700); err != nil {
			return Manifest{}, err
		}
		artifactPath, err = safePath(m.Root, artifactName)
		if err != nil {
			return Manifest{}, err
		}
		temporaryDirectory = filepath.Dir(artifactPath)
	}
	temporary, err := os.CreateTemp(temporaryDirectory, ".snapshot-*")
	if err != nil {
		return Manifest{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	plainHash := sha256.New()
	cipherHash := sha256.New()
	mac := hmac.New(sha256.New, m.artifactMACKey())
	var nonce [aes.BlockSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		temporary.Close()
		return Manifest{}, err
	}
	if _, err := temporary.Write(nonce[:]); err != nil {
		temporary.Close()
		return Manifest{}, err
	}
	mac.Write(nonce[:])
	block, err := aes.NewCipher(m.EncryptionKey)
	if err != nil {
		temporary.Close()
		return Manifest{}, err
	}
	stream := cipher.NewCTR(block, nonce[:])
	var plainBytes int64
	buffer := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			temporary.Close()
			return Manifest{}, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			plainBytes += int64(count)
			if plainBytes > m.MaxBytes {
				temporary.Close()
				return Manifest{}, errors.New("snapshot exceeds size limit")
			}
			plainHash.Write(buffer[:count])
			stream.XORKeyStream(buffer[:count], buffer[:count])
			cipherHash.Write(buffer[:count])
			mac.Write(buffer[:count])
			if _, err := temporary.Write(buffer[:count]); err != nil {
				temporary.Close()
				return Manifest{}, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			temporary.Close()
			return Manifest{}, readErr
		}
	}
	if _, err := temporary.Write(mac.Sum(nil)); err != nil {
		temporary.Close()
		return Manifest{}, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return Manifest{}, err
	}
	if err := temporary.Close(); err != nil {
		return Manifest{}, err
	}
	if m.ObjectStore == nil {
		if err := os.Rename(temporaryPath, artifactPath); err != nil {
			return Manifest{}, err
		}
	} else {
		upload, err := os.Open(temporaryPath)
		if err != nil {
			return Manifest{}, err
		}
		defer func() {
			returnErr = errors.Join(returnErr, upload.Close())
		}()
		info, err := upload.Stat()
		if err != nil {
			return Manifest{}, err
		}
		if _, err := m.ObjectStore.Put(ctx, artifactName, info.Size(), upload); err != nil {
			return Manifest{}, err
		}
	}
	metadata.FormatVersion, metadata.ManifestVersion = 1, 1
	metadata.Artifact = artifactName
	metadata.KeyID = m.KeyID
	metadata.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metadata.PlaintextSHA256 = hex.EncodeToString(plainHash.Sum(nil))
	metadata.CiphertextSHA256 = hex.EncodeToString(cipherHash.Sum(nil))
	metadata.PlaintextBytes = plainBytes
	metadata.CiphertextBytes = plainBytes
	if err := m.verifyArtifact(ctx, metadata); err != nil {
		return Manifest{}, err
	}
	metadata.VerifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metadata.Signature = m.sign(metadata)
	if err := m.publishManifest(ctx, metadata); err != nil {
		return Manifest{}, err
	}
	return metadata, nil
}

func (m SnapshotManager) ensureMonotonicGeneration(ctx context.Context, next uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.ObjectStore != nil {
		body, err := m.readObject(ctx, "current.json")
		if errors.Is(err, blob.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read current snapshot manifest: %w", err)
		}
		var current Manifest
		if err := json.Unmarshal(body, &current); err != nil {
			return fmt.Errorf("decode current snapshot manifest: %w", err)
		}
		if err := m.verifyManifest(ctx, current); err != nil {
			return fmt.Errorf("verify current snapshot manifest: %w", err)
		}
		if current.Generation >= next {
			return fmt.Errorf("snapshot generation %d is not newer than current generation %d", next, current.Generation)
		}
		return nil
	}
	path, err := safePath(m.Root, "current.json")
	if err != nil {
		return err
	}
	body, err := readRegularFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read current snapshot manifest: %w", err)
	}
	var current Manifest
	if err := json.Unmarshal(body, &current); err != nil {
		return fmt.Errorf("decode current snapshot manifest: %w", err)
	}
	if err := m.verifyManifest(ctx, current); err != nil {
		return fmt.Errorf("verify current snapshot manifest: %w", err)
	}
	if current.Generation >= next {
		return fmt.Errorf("snapshot generation %d is not newer than current generation %d", next, current.Generation)
	}
	return nil
}

func (m SnapshotManager) readObject(ctx context.Context, key string) (returnBody []byte, returnErr error) {
	object, reader, err := m.ObjectStore.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, reader.Close())
	}()
	if object.Size > m.MaxBytes+aes.BlockSize+sha256.Size {
		return nil, errors.New("stored snapshot object exceeds the configured size limit")
	}
	body, err := io.ReadAll(io.LimitReader(reader, object.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != object.Size {
		return nil, errors.New("stored object length does not match provider metadata")
	}
	return body, nil
}

func (m SnapshotManager) Restore(ctx context.Context, manifest Manifest, outputPath string) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.verifyManifest(ctx, manifest); err != nil {
		return err
	}
	input, err := m.openArtifact(ctx, manifest.Artifact)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, input.Close())
	}()
	var nonce [aes.BlockSize]byte
	if _, err := io.ReadFull(input, nonce[:]); err != nil {
		return err
	}
	block, err := aes.NewCipher(m.EncryptionKey)
	if err != nil {
		return err
	}
	stream := cipher.NewCTR(block, nonce[:])
	temporary, err := os.CreateTemp(filepath.Dir(outputPath), ".restore-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	var total int64
	buffer := make([]byte, 128*1024)
	remaining := manifest.CiphertextBytes
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			temporary.Close()
			return err
		}
		want := int64(len(buffer))
		if want > remaining {
			want = remaining
		}
		count, err := io.ReadFull(input, buffer[:want])
		if err != nil {
			temporary.Close()
			return err
		}
		stream.XORKeyStream(buffer[:count], buffer[:count])
		total += int64(count)
		if total > m.MaxBytes {
			temporary.Close()
			return errors.New("restored snapshot exceeds size limit")
		}
		hash.Write(buffer[:count])
		if _, err := temporary.Write(buffer[:count]); err != nil {
			temporary.Close()
			return err
		}
		remaining -= int64(count)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != manifest.PlaintextSHA256 || total != manifest.PlaintextBytes {
		return errors.New("restored snapshot digest mismatch")
	}
	return os.Rename(temporaryPath, outputPath)
}

func (m SnapshotManager) openArtifact(ctx context.Context, artifact string) (io.ReadCloser, error) {
	if m.ObjectStore != nil {
		_, reader, err := m.ObjectStore.Open(ctx, artifact)
		return reader, err
	}
	path, err := safePath(m.Root, artifact)
	if err != nil {
		return nil, err
	}
	return openRegularFile(path)
}

func (m SnapshotManager) Current(ctx context.Context, generation uint64) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if m.ObjectStore != nil {
		body, err := m.readObject(ctx, "current.json")
		if err != nil {
			return Manifest{}, err
		}
		var manifest Manifest
		if err := json.Unmarshal(body, &manifest); err != nil {
			return Manifest{}, err
		}
		if manifest.Generation == 0 || manifest.Generation > generation {
			return Manifest{}, errors.New("snapshot generation is newer than the recovery fence")
		}
		if err := m.verifyManifest(ctx, manifest); err != nil {
			return Manifest{}, err
		}
		return manifest, nil
	}
	path, err := safePath(m.Root, "current.json")
	if err != nil {
		return Manifest{}, err
	}
	body, err := readRegularFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Generation == 0 || manifest.Generation > generation {
		return Manifest{}, errors.New("snapshot generation is newer than the recovery fence")
	}
	if err := m.verifyManifest(ctx, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m SnapshotManager) LastVerified(ctx context.Context, maxGeneration uint64) (returnManifest Manifest, returnErr error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if m.ObjectStore != nil {
		var newest Manifest
		err := m.ObjectStore.Walk(ctx, "manifests/", func(object blob.Object) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !strings.HasSuffix(object.Key, ".json") {
				return nil
			}
			body, err := m.readObject(ctx, object.Key)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				if !errors.Is(err, blob.ErrNotFound) {
					return err
				}
				return nil
			}
			var manifest Manifest
			if json.Unmarshal(body, &manifest) != nil || manifest.Generation == 0 || manifest.Generation > maxGeneration || manifest.Generation <= newest.Generation {
				return nil
			}
			if err := m.verifyManifest(ctx, manifest); err != nil {
				if !isSnapshotIntegrityError(err) {
					return err
				}
				return nil
			}
			newest = manifest
			return nil
		})
		if err != nil {
			return Manifest{}, err
		}
		if newest.Generation > 0 {
			return newest, nil
		}
		return Manifest{}, errors.New("no verified snapshot is available at or before recovery generation")
	}
	manifestDirectory, err := safePath(m.Root, "manifests")
	if err != nil {
		return Manifest{}, err
	}
	var newest Manifest
	directory, err := os.Open(manifestDirectory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, err
	}
	if err == nil {
		defer func() {
			returnErr = errors.Join(returnErr, directory.Close())
		}()
		for {
			if err := ctx.Err(); err != nil {
				return Manifest{}, err
			}
			entries, readErr := directory.ReadDir(128)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return Manifest{}, err
				}
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				body, readErr := readRegularFile(filepath.Join(manifestDirectory, entry.Name()))
				if readErr != nil {
					if !errors.Is(readErr, os.ErrNotExist) {
						return Manifest{}, readErr
					}
					continue
				}
				var manifest Manifest
				if json.Unmarshal(body, &manifest) != nil || manifest.Generation == 0 || manifest.Generation > maxGeneration || manifest.Generation <= newest.Generation {
					continue
				}
				if err := m.verifyManifest(ctx, manifest); err != nil {
					if !isSnapshotIntegrityError(err) {
						return Manifest{}, err
					}
					continue
				}
				newest = manifest
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
			if readErr != nil {
				return Manifest{}, readErr
			}
		}
	}
	if newest.Generation > 0 {
		return newest, nil
	}
	{
		currentPath, pathErr := safePath(m.Root, "current.json")
		if pathErr != nil {
			return Manifest{}, pathErr
		}
		body, readErr := readRegularFile(currentPath)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return Manifest{}, readErr
		}
		if readErr == nil {
			var manifest Manifest
			if json.Unmarshal(body, &manifest) == nil && manifest.Generation > 0 && manifest.Generation <= maxGeneration {
				if err := m.verifyManifest(ctx, manifest); err != nil {
					if !isSnapshotIntegrityError(err) {
						return Manifest{}, err
					}
				} else {
					return manifest, nil
				}
			}
		}
	}
	return Manifest{}, errors.New("no verified snapshot is available at or before recovery generation")
}

func (m SnapshotManager) verifyManifest(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if manifest.FormatVersion != 1 || manifest.ManifestVersion != 1 || manifest.Generation == 0 || manifest.Backend == "" || manifest.SchemaVersion < 1 {
		return invalidSnapshot("snapshot manifest format or metadata is invalid")
	}
	if manifest.PlaintextBytes < 0 || manifest.CiphertextBytes != manifest.PlaintextBytes || len(manifest.PlaintextSHA256) != sha256.Size*2 || len(manifest.CiphertextSHA256) != sha256.Size*2 {
		return invalidSnapshot("snapshot manifest size or digest metadata is invalid")
	}
	if _, err := hex.DecodeString(manifest.PlaintextSHA256); err != nil {
		return invalidSnapshot("snapshot manifest plaintext digest is invalid")
	}
	if _, err := hex.DecodeString(manifest.CiphertextSHA256); err != nil {
		return invalidSnapshot("snapshot manifest ciphertext digest is invalid")
	}
	if strings.TrimSpace(manifest.CreatedAt) == "" || strings.TrimSpace(manifest.VerifiedAt) == "" || strings.TrimSpace(manifest.Artifact) == "" {
		return invalidSnapshot("snapshot manifest provenance is incomplete")
	}
	if manifest.KeyID != m.KeyID || manifest.Signature == "" {
		return invalidSnapshot("snapshot manifest authentication failed")
	}
	if !hmac.Equal([]byte(manifest.Signature), []byte(m.sign(manifest))) {
		return invalidSnapshot("snapshot manifest signature mismatch")
	}
	return m.verifyArtifact(ctx, manifest)
}

func (m SnapshotManager) verifyArtifact(ctx context.Context, manifest Manifest) (returnErr error) {
	input, err := m.openArtifact(ctx, manifest.Artifact)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return invalidSnapshot("snapshot artifact is missing")
		}
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, input.Close())
	}()
	objectSize := int64(-1)
	if m.ObjectStore != nil {
		object, sizeReader, err := m.ObjectStore.Open(ctx, manifest.Artifact)
		if err != nil {
			return err
		}
		closeErr := sizeReader.Close()
		if closeErr != nil {
			return closeErr
		}
		objectSize = object.Size
	} else {
		statInput, ok := input.(interface{ Stat() (os.FileInfo, error) })
		if !ok {
			return errors.New("filesystem snapshot artifact does not expose file metadata")
		}
		stat, err := statInput.Stat()
		if err != nil {
			return err
		}
		objectSize = stat.Size()
	}
	if objectSize != manifest.CiphertextBytes+aes.BlockSize+sha256.Size {
		return invalidSnapshot("snapshot artifact size mismatch")
	}
	var nonce [aes.BlockSize]byte
	if _, err := io.ReadFull(input, nonce[:]); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, m.artifactMACKey())
	mac.Write(nonce[:])
	hash := sha256.New()
	remaining := manifest.CiphertextBytes
	buffer := make([]byte, 128*1024)
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		want := int64(len(buffer))
		if want > remaining {
			want = remaining
		}
		count, err := io.CopyN(io.MultiWriter(mac, hash), input, want)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return invalidSnapshot("snapshot artifact is truncated")
			}
			return err
		}
		remaining -= count
	}
	provided := make([]byte, sha256.Size)
	if _, err := io.ReadFull(input, provided); err != nil {
		if errors.Is(err, io.EOF) {
			return invalidSnapshot("snapshot artifact authentication tag is truncated")
		}
		return err
	}
	if !hmac.Equal(provided, mac.Sum(nil)) || hex.EncodeToString(hash.Sum(nil)) != manifest.CiphertextSHA256 {
		return invalidSnapshot("snapshot artifact authentication failed")
	}
	return nil
}

func (m SnapshotManager) publishManifest(ctx context.Context, manifest Manifest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if m.ObjectStore != nil {
		manifestKey := fmt.Sprintf("manifests/%020d.json", manifest.Generation)
		if _, err := m.ObjectStore.Put(ctx, manifestKey, int64(len(body)), bytes.NewReader(body)); err != nil {
			return err
		}
		_, err := m.ObjectStore.Put(ctx, "current.json", int64(len(body)), bytes.NewReader(body))
		return err
	}
	path, err := safePath(m.Root, fmt.Sprintf("manifests/%020d.json", manifest.Generation))
	if err != nil {
		return err
	}
	if err := atomicWrite(path, body); err != nil {
		return err
	}
	current, err := safePath(m.Root, "current.json")
	if err != nil {
		return err
	}
	return atomicWrite(current, body)
}

func (m SnapshotManager) sign(manifest Manifest) string {
	manifest.Signature = ""
	body, _ := json.Marshal(manifest)
	mac := hmac.New(sha512.New, m.SigningKey)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func (m SnapshotManager) artifactMACKey() []byte {
	sum := sha256.Sum256(append([]byte("sameoldchat-artifact-mac\x00"), m.EncryptionKey...))
	return sum[:]
}

func safePath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" || strings.Contains(relative, "..") {
		return "", errors.New("unsafe snapshot path")
	}
	path := filepath.Join(root, relative)
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot root: %w", err)
	}
	cleanPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot path: %w", err)
	}
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", errors.New("snapshot path escapes root")
	}
	rootInfo, err := os.Lstat(cleanRoot)
	if err == nil {
		if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
			return "", errors.New("snapshot root is not a directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	relativePath, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot relative path: %w", err)
	}
	current := cleanRoot
	for index, part := range strings.Split(relativePath, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("snapshot path component %q is a symlink", part)
		}
		if index < len(strings.Split(relativePath, string(os.PathSeparator)))-1 && !info.IsDir() {
			return "", fmt.Errorf("snapshot path component %q is not a directory", part)
		}
	}
	return path, nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("snapshot path %q is not a regular file", path)
	}
	return os.ReadFile(path)
}

func openRegularFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("snapshot path %q is not a regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err = file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(fmt.Errorf("snapshot path %q is not a regular file", path), file.Close())
	}
	return file, nil
}

func atomicWrite(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".atomic-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	closed := false
	cleanup := func(cause error) error {
		var cleanupErr error
		if !closed {
			cleanupErr = file.Close()
			closed = true
		}
		cleanupErr = errors.Join(cleanupErr, os.Remove(temp))
		return errors.Join(cause, cleanupErr)
	}
	if _, err := file.Write(body); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		closed = true
		return errors.Join(err, os.Remove(temp))
	}
	closed = true
	if err := os.Rename(temp, path); err != nil {
		return errors.Join(err, os.Remove(temp))
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
