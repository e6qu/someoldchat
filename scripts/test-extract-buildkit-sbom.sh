#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
archive="$(mktemp)"
output="$(mktemp)"
trap 'rm -rf "$fixture"; rm -f "$archive" "$output"' EXIT
mkdir -p "$fixture/blobs/sha256"

subject="sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
top="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
attestation="cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
statement="dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

printf '%s\n' '{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:'"$top"'","size":1}]}' >"$fixture/index.json"
printf '%s\n' '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:'"$attestation"'","size":1,"annotations":{"vnd.docker.reference.digest":"'"$subject"'","vnd.docker.reference.type":"attestation-manifest"},"platform":{"architecture":"unknown","os":"unknown"}}]}' >"$fixture/blobs/sha256/$top"
printf '%s\n' '{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[{"mediaType":"application/vnd.in-toto+json","digest":"sha256:'"$statement"'","size":1,"annotations":{"in-toto.io/predicate-type":"https://spdx.dev/Document"}}]}' >"$fixture/blobs/sha256/$attestation"
printf '%s\n' '{"_type":"https://in-toto.io/Statement/v1","predicateType":"https://spdx.dev/Document","subject":[],"predicate":{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"qualified fixture","packages":[]}}' >"$fixture/blobs/sha256/$statement"
tar -cf "$archive" -C "$fixture" .

"$root/scripts/extract-buildkit-sbom.sh" "$archive" "$output" "$subject"
jq -e '.spdxVersion == "SPDX-2.3" and .SPDXID == "SPDXRef-DOCUMENT" and .name == "qualified fixture"' "$output" >/dev/null

jq '.predicate.spdxVersion = "SPDX-2.2"' "$fixture/blobs/sha256/$statement" >"$output"
mv "$output" "$fixture/blobs/sha256/$statement"
tar -cf "$archive" -C "$fixture" .
if "$root/scripts/extract-buildkit-sbom.sh" "$archive" "$output" "$subject" 2>/dev/null; then
	echo 'BuildKit SBOM with an unsupported SPDX version was accepted' >&2
	exit 1
fi

echo 'BuildKit SPDX SBOM extraction passed'
