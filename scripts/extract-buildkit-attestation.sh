#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: extract-buildkit-attestation.sh <oci-archive> <output> <image-digest> <predicate-type>}"
output="${2:?usage: extract-buildkit-attestation.sh <oci-archive> <output> <image-digest> <predicate-type>}"
image_digest="${3:?usage: extract-buildkit-attestation.sh <oci-archive> <output> <image-digest> <predicate-type>}"
predicate_type="${4:?usage: extract-buildkit-attestation.sh <oci-archive> <output> <image-digest> <predicate-type>}"

if [[ ! "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
	echo "image digest must be a SHA-256 digest: $image_digest" >&2
	exit 1
fi
case "$predicate_type" in
	https://slsa.dev/provenance/v1 | https://spdx.dev/Document) ;;
	*)
		echo "unsupported BuildKit predicate type: $predicate_type" >&2
		exit 1
		;;
esac

layout="$(mktemp -d)"
trap 'rm -rf "$layout"' EXIT
tar -xf "$archive" -C "$layout"

blob_path() {
	local digest="$1"
	if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
		echo "OCI layout contained an invalid digest: $digest" >&2
		return 1
	fi
	printf '%s/blobs/sha256/%s' "$layout" "${digest#sha256:}"
}

top_digest="$(jq -er 'if (.manifests | length) == 1 then .manifests[0].digest else error("expected one OCI layout root") end' "$layout/index.json")"
top_blob="$(blob_path "$top_digest")"
attestation_digest="$(jq -er --arg subject "$image_digest" '
  [.manifests[]
   | select(.annotations["vnd.docker.reference.type"] == "attestation-manifest")
   | select(.annotations["vnd.docker.reference.digest"] == $subject)
   | .digest]
  | if length == 1 then .[0] else error("expected one BuildKit attestation manifest") end
' "$top_blob")"
attestation_blob="$(blob_path "$attestation_digest")"
statement_digest="$(jq -er --arg predicate "$predicate_type" '
  [.layers[]
   | select(.mediaType == "application/vnd.in-toto+json")
   | select(.annotations["in-toto.io/predicate-type"] == $predicate)
   | .digest]
  | if length == 1 then .[0] else error("expected one matching in-toto statement") end
' "$attestation_blob")"
statement_blob="$(blob_path "$statement_digest")"

if ! jq -e --arg predicate "$predicate_type" '
  ._type == "https://in-toto.io/Statement/v1"
  and .predicateType == $predicate
  and (
    ($predicate == "https://spdx.dev/Document"
      and .predicate.spdxVersion == "SPDX-2.3"
      and .predicate.SPDXID == "SPDXRef-DOCUMENT")
    or
    ($predicate == "https://slsa.dev/provenance/v1"
      and (.predicate.buildDefinition.buildType | type == "string" and length > 0)
      and (.predicate.buildDefinition.externalParameters | type == "object")
      and (.predicate.buildDefinition.internalParameters | type == "object")
      and (.predicate.buildDefinition.resolvedDependencies | type == "array")
      and (.predicate.runDetails.builder.id | type == "string" and test("^https://[^[:space:]]+$"))
      and (.predicate.runDetails.metadata | type == "object"))
  )
' "$statement_blob" >/dev/null; then
	echo "BuildKit statement was not a valid $predicate_type predicate" >&2
	exit 1
fi

jq '.predicate' "$statement_blob" >"$output"
