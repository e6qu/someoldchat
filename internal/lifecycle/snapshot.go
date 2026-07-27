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

// ErrNoVerifiedSnapshot reports that the requested generation has no published
// manifest at all. It is a distinct sentinel because "nothing has been
// snapshotted yet" is an ordinary state during a first hibernation, while an
// unreadable snapshot provider is not.
var ErrNoVerifiedSnapshot = errors.New("no snapshot manifest is published for the selected generation")

// integrityError marks a deterministic defect in stored snapshot bytes. It is
// deliberately distinct from provider availability errors: only the former
// proves a generation bad and may be quarantined. A timeout or denied request
// must fail recovery without rewriting the durable verdict.
type integrityError struct {
	reason string
}

func (e integrityError) Error() string { return e.reason }

func integrityFailure(reason string) error {
	return integrityError{reason: reason}
}

// SnapshotManager stores encrypted, signed snapshot artifacts and manifests in
// exactly one durable target: either a filesystem Root or an ObjectStore.
// Validate enforces that invariant so the two shapes cannot drift apart again.
//
// The object target is a plain blob.Store rather than a blob.WalkStore: every
// record this manager reads is addressed by the generation that names it, so it
// never enumerates the bucket. Enumeration was only ever needed by the
// "newest verified generation at or before N" scan, which was the implicit
// fallback specs/scale-to-zero.md forbids; Select replaced it.
type SnapshotManager struct {
	Root          string
	ObjectStore   blob.Store
	EncryptionKey []byte
	SigningKey    []byte
	KeyID         string
	MaxBytes      int64
}

func NewSnapshotManager(root string, encryptionKey, signingKey []byte, keyID string, maxBytes int64) (SnapshotManager, error) {
	manager := SnapshotManager{Root: root, EncryptionKey: append([]byte(nil), encryptionKey...), SigningKey: append([]byte(nil), signingKey...), KeyID: keyID, MaxBytes: maxBytes}
	if err := manager.Validate(); err != nil {
		return SnapshotManager{}, err
	}
	return manager, nil
}

func NewObjectSnapshotManager(store blob.Store, encryptionKey, signingKey []byte, keyID string, maxBytes int64) (SnapshotManager, error) {
	if store == nil {
		return SnapshotManager{}, errors.New("object snapshot manager requires an object store")
	}
	manager := SnapshotManager{ObjectStore: store, EncryptionKey: append([]byte(nil), encryptionKey...), SigningKey: append([]byte(nil), signingKey...), KeyID: keyID, MaxBytes: maxBytes}
	if err := manager.Validate(); err != nil {
		return SnapshotManager{}, err
	}
	return manager, nil
}

// Validate reports whether the manager names exactly one durable snapshot
// target and carries complete key material. Every consumer uses it, so an
// object-store manager and a filesystem manager are accepted or rejected by one
// rule instead of by separately drifting guards.
func (m SnapshotManager) Validate() error {
	hasRoot := strings.TrimSpace(m.Root) != ""
	if hasRoot && !filepath.IsAbs(m.Root) {
		return errors.New("snapshot root must be an absolute path")
	}
	if hasRoot == (m.ObjectStore != nil) {
		return errors.New("snapshot manager requires exactly one of an absolute filesystem root or an object store")
	}
	if len(m.EncryptionKey) != 32 || len(m.SigningKey) < 32 {
		return errors.New("snapshot keys are too short")
	}
	if strings.TrimSpace(m.KeyID) == "" || m.MaxBytes <= 0 {
		return errors.New("snapshot key ID and positive size limit are required")
	}
	return nil
}

// stagingDirectory is where large temporary artifacts are built before they are
// promoted. A filesystem manager stages beside its final location so promotion
// is a rename; an object-store manager has no local namespace and uses the
// system temporary directory.
func (m SnapshotManager) stagingDirectory() string {
	if m.ObjectStore != nil {
		return os.TempDir()
	}
	return m.Root
}

func (m SnapshotManager) Create(sourcePath string, metadata Manifest) (Manifest, error) {
	if metadata.Generation == 0 || metadata.Backend == "" || metadata.SchemaVersion < 1 {
		return Manifest{}, errors.New("snapshot metadata is incomplete")
	}
	// The current manifest is read once, and it is its *record* — format,
	// metadata, and signature — that must verify here. Verifying its artifact as
	// well made one rotted artifact permanently fatal: every later hibernation
	// failed at "verify current snapshot manifest", so a deployment whose newest
	// snapshot had lost a byte could never publish a good one again and had no
	// way back except deleting the durable record by hand. Whether those bytes
	// are still readable is a question for the restore that selects them, and
	// Select answers it by quarantining the generation.
	previous, err := m.currentRecord()
	switch {
	case err == nil:
		if previous.Generation >= metadata.Generation {
			return Manifest{}, fmt.Errorf("snapshot generation %d is not newer than current generation %d", metadata.Generation, previous.Generation)
		}
		metadata.PreviousGeneration = previous.Generation
	case errors.Is(err, os.ErrNotExist):
	default:
		return Manifest{}, fmt.Errorf("read current snapshot manifest: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return Manifest{}, err
	}
	defer source.Close()
	artifactName := fmt.Sprintf("artifacts/%020d.bin", metadata.Generation)
	temporaryDirectory := os.TempDir()
	artifactPath := ""
	if m.ObjectStore == nil {
		// Only the artifact directory is created here; writeRecord creates the
		// directory of every control record it publishes.
		if err := os.MkdirAll(filepath.Join(m.Root, "artifacts"), 0o700); err != nil {
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
		// The manifest publication below is fsynced, so without syncing the
		// artifact directory a power loss can leave a durable current.json
		// naming an artifact that never reached the disk.
		if err := syncDirectory(filepath.Dir(artifactPath)); err != nil {
			return Manifest{}, err
		}
	} else {
		upload, err := os.Open(temporaryPath)
		if err != nil {
			return Manifest{}, err
		}
		defer upload.Close()
		info, err := upload.Stat()
		if err != nil {
			return Manifest{}, err
		}
		if _, err := m.ObjectStore.Put(context.Background(), artifactName, info.Size(), upload); err != nil {
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
	if err := m.verifyArtifact(metadata); err != nil {
		return Manifest{}, err
	}
	metadata.VerifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if metadata.Signature, err = m.signManifest(metadata); err != nil {
		return Manifest{}, err
	}
	if err := m.publishManifest(metadata); err != nil {
		return Manifest{}, err
	}
	return metadata, nil
}

// currentRecord reads and record-verifies the published current manifest. It
// does not read the artifact; see Create for why publication must not depend on
// bytes an older generation is responsible for.
func (m SnapshotManager) currentRecord() (Manifest, error) {
	body, err := m.readRecord("current.json")
	if err != nil {
		return Manifest{}, err
	}
	var current Manifest
	if err := json.Unmarshal(body, &current); err != nil {
		return Manifest{}, fmt.Errorf("decode current snapshot manifest: %w", err)
	}
	if err := m.verifyManifestRecord(current); err != nil {
		return Manifest{}, fmt.Errorf("verify current snapshot manifest: %w", err)
	}
	return current, nil
}

// readRecord reads one small control record from whichever durable target is
// configured, and reports a missing record as os.ErrNotExist for both backends.
// One absence sentinel is what lets every caller distinguish "nothing has been
// published" from "the store cannot be read", which is the distinction the whole
// recovery policy turns on.
func (m SnapshotManager) readRecord(key string) ([]byte, error) {
	if m.ObjectStore != nil {
		body, err := m.readObject(key)
		if errors.Is(err, blob.ErrNotFound) {
			return nil, fmt.Errorf("snapshot record %q: %w", key, os.ErrNotExist)
		}
		return body, err
	}
	path, err := safePath(m.Root, key)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// writeRecord publishes one small control record durably: atomically and
// fsynced on a filesystem, as a single object put against an object store.
func (m SnapshotManager) writeRecord(key string, body []byte) error {
	if m.ObjectStore != nil {
		_, err := m.ObjectStore.Put(context.Background(), key, int64(len(body)), bytes.NewReader(body))
		return err
	}
	path, err := safePath(m.Root, key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWrite(path, body)
}

func (m SnapshotManager) readObject(key string) ([]byte, error) {
	object, reader, err := m.ObjectStore.Open(context.Background(), key)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
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

func (m SnapshotManager) Restore(manifest Manifest, outputPath string) error {
	if err := m.verifyAndRecord(manifest); err != nil {
		return err
	}
	input, err := m.openArtifact(manifest.Artifact)
	if err != nil {
		return err
	}
	defer input.Close()
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
		return m.quarantineFailure(manifest.Generation, integrityFailure("restored snapshot digest mismatch"))
	}
	return os.Rename(temporaryPath, outputPath)
}

func (m SnapshotManager) openArtifact(artifact string) (io.ReadCloser, error) {
	if m.ObjectStore != nil {
		_, reader, err := m.ObjectStore.Open(context.Background(), artifact)
		return reader, err
	}
	path, err := safePath(m.Root, artifact)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// Current returns the published current manifest, fully verified, provided it is
// not newer than the recovery fence.
//
// A verification failure here is recorded against the generation exactly as one
// discovered by Select is: the wake path is the other place a generation is
// proved bad, and a failure it found must not be forgotten just because the
// operator was not the one who found it.
func (m SnapshotManager) Current(generation uint64) (Manifest, error) {
	body, err := m.readRecord("current.json")
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
	if err := m.verifyAndRecord(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Select loads exactly the requested generation and verifies it.
//
// The former implementation searched backward for the newest usable generation.
// Even though the coordinator later rejected a different generation, that scan
// made fallback part of the storage contract, required object-store
// enumeration, and never durably recorded the corrupt generation it skipped.
// Explicit operator recovery names one generation, so direct addressing is
// both the smaller interface and the only answer consistent with the recovery
// policy.
func (m SnapshotManager) Select(generation uint64) (Manifest, error) {
	if generation == 0 {
		return Manifest{}, ErrNoVerifiedSnapshot
	}
	body, err := m.readRecord(manifestKey(generation))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("%w: generation %d", ErrNoVerifiedSnapshot, generation)
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		integrityErr := integrityFailure("decode selected snapshot manifest: " + err.Error())
		return Manifest{}, m.quarantineFailure(generation, integrityErr)
	}
	if manifest.Generation != generation {
		integrityErr := integrityFailure(fmt.Sprintf("selected snapshot manifest names generation %d, want %d", manifest.Generation, generation))
		return Manifest{}, m.quarantineFailure(generation, integrityErr)
	}
	if err := m.verifyAndRecord(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// verifyAndRecord verifies a published generation and quarantines only a
// deterministic integrity failure. Provider failures remain retryable and do
// not become evidence that the generation's bytes are bad.
func (m SnapshotManager) verifyAndRecord(manifest Manifest) error {
	err := m.verifyManifest(manifest)
	if err == nil {
		return nil
	}
	var integrity integrityError
	if !errors.As(err, &integrity) {
		return err
	}
	return m.quarantineFailure(manifest.Generation, err)
}

type quarantineRecord struct {
	Generation uint64 `json:"generation"`
	DetectedAt string `json:"detected_at"`
	Reason     string `json:"reason"`
}

func (m SnapshotManager) quarantineFailure(generation uint64, failure error) error {
	if generation == 0 {
		return failure
	}
	record := quarantineRecord{
		Generation: generation,
		DetectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Reason:     failure.Error(),
	}
	body, err := json.Marshal(record)
	if err != nil {
		return errors.Join(failure, fmt.Errorf("encode snapshot quarantine record: %w", err))
	}
	key := fmt.Sprintf("quarantine/%020d.json", generation)
	if err := m.writeRecord(key, body); err != nil {
		return errors.Join(failure, fmt.Errorf("publish snapshot quarantine record: %w", err))
	}
	return failure
}

// verifyManifest proves a generation is restorable: its record is authentic and
// its artifact still holds exactly the bytes the record names.
func (m SnapshotManager) verifyManifest(manifest Manifest) error {
	if err := m.verifyManifestRecord(manifest); err != nil {
		return err
	}
	return m.verifyArtifact(manifest)
}

// verifyManifestRecord proves the manifest itself is authentic and internally
// consistent, without reading the artifact. Every failure it can report is a
// property of the stored record rather than of the store, so each is an
// integrityError: re-reading the same bytes produces the same verdict, which is
// what makes a generation quarantinable rather than merely unavailable.
func (m SnapshotManager) verifyManifestRecord(manifest Manifest) error {
	if manifest.FormatVersion != 1 || manifest.ManifestVersion != 1 || manifest.Generation == 0 || manifest.Backend == "" || manifest.SchemaVersion < 1 {
		return integrityFailure("snapshot manifest format or metadata is invalid")
	}
	if manifest.PlaintextBytes < 0 || manifest.CiphertextBytes != manifest.PlaintextBytes || len(manifest.PlaintextSHA256) != sha256.Size*2 || len(manifest.CiphertextSHA256) != sha256.Size*2 {
		return integrityFailure("snapshot manifest size or digest metadata is invalid")
	}
	if _, err := hex.DecodeString(manifest.PlaintextSHA256); err != nil {
		return integrityFailure("snapshot manifest plaintext digest is invalid")
	}
	if _, err := hex.DecodeString(manifest.CiphertextSHA256); err != nil {
		return integrityFailure("snapshot manifest ciphertext digest is invalid")
	}
	if strings.TrimSpace(manifest.CreatedAt) == "" || strings.TrimSpace(manifest.VerifiedAt) == "" || strings.TrimSpace(manifest.Artifact) == "" {
		return integrityFailure("snapshot manifest provenance is incomplete")
	}
	if manifest.KeyID != m.KeyID || manifest.Signature == "" {
		return integrityFailure("snapshot manifest authentication failed")
	}
	signature, err := m.signManifest(manifest)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(manifest.Signature), []byte(signature)) {
		return integrityFailure("snapshot manifest signature mismatch")
	}
	return nil
}

func (m SnapshotManager) verifyArtifact(manifest Manifest) error {
	var input io.ReadCloser
	var objectSize int64
	if m.ObjectStore != nil {
		object, reader, err := m.ObjectStore.Open(context.Background(), manifest.Artifact)
		if err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return integrityFailure("snapshot artifact is missing")
			}
			return err
		}
		input = reader
		objectSize = object.Size
	} else {
		path, err := safePath(m.Root, manifest.Artifact)
		if err != nil {
			return integrityFailure(err.Error())
		}
		file, err := os.Open(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return integrityFailure("snapshot artifact is missing")
			}
			return err
		}
		input = file
		stat, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return err
		}
		objectSize = stat.Size()
	}
	defer input.Close()
	if objectSize != manifest.CiphertextBytes+aes.BlockSize+sha256.Size {
		return integrityFailure("snapshot artifact size mismatch")
	}
	var nonce [aes.BlockSize]byte
	if _, err := io.ReadFull(input, nonce[:]); err != nil {
		return integrityFailure("snapshot artifact nonce is truncated")
	}
	mac := hmac.New(sha256.New, m.artifactMACKey())
	mac.Write(nonce[:])
	hash := sha256.New()
	remaining := manifest.CiphertextBytes
	buffer := make([]byte, 128*1024)
	for remaining > 0 {
		want := int64(len(buffer))
		if want > remaining {
			want = remaining
		}
		count, err := io.CopyN(io.MultiWriter(mac, hash), input, want)
		if err != nil {
			return integrityFailure("snapshot artifact ciphertext is truncated")
		}
		remaining -= count
	}
	provided := make([]byte, sha256.Size)
	if _, err := io.ReadFull(input, provided); err != nil {
		return integrityFailure("snapshot artifact authentication tag is truncated")
	}
	if !hmac.Equal(provided, mac.Sum(nil)) || hex.EncodeToString(hash.Sum(nil)) != manifest.CiphertextSHA256 {
		return integrityFailure("snapshot artifact authentication failed")
	}
	return nil
}

func (m SnapshotManager) publishManifest(manifest Manifest) error {
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// The generation's own manifest is published before it is selected as
	// current, so a current.json is never the only record of a generation.
	if err := m.writeRecord(manifestKey(manifest.Generation), body); err != nil {
		return err
	}
	return m.writeRecord("current.json", body)
}

// manifestKey addresses a generation's published manifest. Both backends use one
// key space, which is what lets Select read a generation directly instead of
// enumerating and choosing.
func manifestKey(generation uint64) string {
	return fmt.Sprintf("manifests/%020d.json", generation)
}

// signManifest authenticates the manifest. The marshal error is surfaced rather
// than dropped: an empty body would produce a perfectly valid signature over
// nothing, which is the one failure an authentication boundary must never
// silently accept.
func (m SnapshotManager) signManifest(manifest Manifest) (string, error) {
	manifest.Signature = ""
	body, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("sign snapshot manifest: %w", err)
	}
	mac := hmac.New(sha512.New, m.SigningKey)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
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
	cleanRoot, _ := filepath.Abs(root)
	cleanPath, _ := filepath.Abs(path)
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", errors.New("snapshot path escapes root")
	}
	return path, nil
}
func atomicWrite(path string, body []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".atomic-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
