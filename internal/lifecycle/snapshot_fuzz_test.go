package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Restore manifests are read back from object storage, which is exactly where
// an attacker with write access would place one. A manifest names the artifact
// to restore and the digests to accept, so verification is the boundary
// deciding whether this process restores someone else's snapshot over the live
// database.
//
// These targets work from a genuinely created snapshot. Verifying a manifest
// that was never backed by an artifact fails on the missing artifact long
// before the signature is consulted, so a target built that way would pass no
// matter how badly the signature check were broken.

// fuzzSnapshot builds one real snapshot for a whole target. Creating it per
// iteration would dominate the run: encryption, hashing and file writes cost
// far more than the verification under test, and the exec rate collapses from
// tens of thousands per second to tens.
func fuzzSnapshot(f *testing.F) (SnapshotManager, Manifest) {
	f.Helper()
	root := f.TempDir()
	manager := SnapshotManager{
		Root:          root,
		EncryptionKey: []byte("0123456789abcdef0123456789abcdef"),
		SigningKey:    []byte("qualification-signing-key"),
		KeyID:         "qualification",
		MaxBytes:      1 << 20,
	}
	source := filepath.Join(root, "source.db")
	if err := os.WriteFile(source, []byte("snapshot contents"), 0o600); err != nil {
		f.Fatal(err)
	}
	manifest, err := manager.Create(source, Manifest{Generation: 1, Backend: "sqlite", SchemaVersion: 1})
	if err != nil {
		f.Fatal(err)
	}
	// Without this the targets below would be vacuous: every manifest would be
	// rejected for reasons unrelated to the property under test.
	if err := manager.verifyManifest(manifest); err != nil {
		f.Fatalf("freshly created manifest does not verify: %v", err)
	}
	return manager, manifest
}

// Decoding a manifest must not panic whatever bytes storage returns. A panic
// here aborts a wake, the moment the process can least afford one.
func FuzzManifestDecodingNeverPanics(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("{}"))
	f.Add([]byte("null"))
	f.Add([]byte("[]"))
	f.Add([]byte(`{"format_version":1,"manifest_version":1,"generation":1}`))
	f.Add([]byte(`{"generation":18446744073709551615}`))
	f.Add([]byte(`{"plaintext_bytes":-1,"ciphertext_bytes":-1}`))
	f.Add([]byte(`{"plaintext_sha256":"zz","ciphertext_sha256":"zz"}`))

	manager, _ := fuzzSnapshot(f)
	f.Fuzz(func(t *testing.T, raw []byte) {
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return
		}
		_ = manager.verifyManifest(manifest)
	})
}

// Re-signing a mutated manifest with a foreign key must not make it verify.
// The manifest here is otherwise genuine and backed by a real artifact, so the
// only thing standing between the mutation and a restore is the signature.
func FuzzManifestVerificationRejectsForeignSignatures(f *testing.F) {
	f.Add("sqlite", "artifacts/00000000000000000001.bin", uint64(1))
	f.Add("dqlite", "artifacts/00000000000000000002.bin", uint64(2))
	f.Add("", "", uint64(0))

	manager, genuine := fuzzSnapshot(f)
	f.Fuzz(func(t *testing.T, backend, artifact string, generation uint64) {
		manifest := genuine
		if backend != "" {
			manifest.Backend = backend
		}
		if artifact != "" {
			manifest.Artifact = artifact
		}
		if generation != 0 {
			manifest.Generation = generation
		}

		forger := manager
		forger.SigningKey = []byte("attacker-signing-key")
		manifest.Signature = forger.sign(manifest)
		if err := manager.verifyManifest(manifest); err == nil {
			t.Fatalf("manifest signed with a foreign key verified: %+v", manifest)
		}

		manifest.Signature = ""
		if err := manager.verifyManifest(manifest); err == nil {
			t.Fatalf("unsigned manifest verified: %+v", manifest)
		}
	})
}

// Mutating a signed field without re-signing must not verify. This is the
// tamper case: an attacker edits a genuine manifest to point at a different
// artifact and leaves the original signature in place.
func FuzzManifestVerificationRejectsTamperedFields(f *testing.F) {
	f.Add("artifacts/other.bin", uint64(99), "postgres")
	f.Add("", uint64(0), "")

	manager, genuine := fuzzSnapshot(f)
	f.Fuzz(func(t *testing.T, artifact string, generation uint64, backend string) {
		manifest := genuine
		original := manifest
		if artifact != "" {
			manifest.Artifact = artifact
		}
		if generation != 0 {
			manifest.Generation = generation
		}
		if backend != "" {
			manifest.Backend = backend
		}
		if manifest == original {
			t.Skip() // nothing was mutated
		}
		// Signature left untouched from the genuine manifest.
		if err := manager.verifyManifest(manifest); err == nil {
			t.Fatalf("tampered manifest verified: %+v", manifest)
		}
	})
}

// A manifest carrying an unrecognised key identifier must be rejected, so
// rotating the signing key retires manifests signed under the old one.
func FuzzManifestVerificationRejectsForeignKeyIDs(f *testing.F) {
	f.Add("other-key")
	f.Add("")
	f.Add("qualification-")

	manager, genuine := fuzzSnapshot(f)
	f.Fuzz(func(t *testing.T, keyID string) {
		manifest := genuine
		if keyID == manager.KeyID {
			t.Skip()
		}
		manifest.KeyID = keyID
		manifest.Signature = manager.sign(manifest)
		if err := manager.verifyManifest(manifest); err == nil {
			t.Fatalf("manifest with foreign key id %q verified", keyID)
		}
	})
}
