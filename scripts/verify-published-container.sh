#!/usr/bin/env bash
set -euo pipefail

image="${1:?usage: verify-published-container.sh <image> <12-character-sha>}"
tag="${2:?usage: verify-published-container.sh <image> <12-character-sha>}"

if [[ ! "$tag" =~ ^[0-9a-f]{12}$ ]]; then
	echo "container tag must be a lowercase 12-character commit SHA: $tag" >&2
	exit 1
fi

# The retry loop used to send every attempt's stderr to /dev/null, so an
# authentication failure, a DNS failure, and genuine registry propagation delay
# were indistinguishable: the job failed after a minute with "was not readable"
# and no cause. The last attempt's diagnostics are kept and reported.
inspect_raw() {
	local reference="$1" raw attempt diagnostics
	diagnostics="$(mktemp)"
	for attempt in {1..12}; do
		if raw="$(docker buildx imagetools inspect --raw "$reference" 2>"$diagnostics")"; then
			rm -f "$diagnostics"
			printf '%s' "$raw"
			return 0
		fi
		sleep 5
	done
	echo "published image was not readable after $attempt attempts: $reference" >&2
	echo "last registry error:" >&2
	sed 's/^/    /' "$diagnostics" >&2
	rm -f "$diagnostics"
	return 1
}

manifest_digest() {
	if command -v sha256sum >/dev/null 2>&1; then
		printf '%s' "$1" | sha256sum | awk '{print "sha256:" $1}'
		return
	fi
	if command -v shasum >/dev/null 2>&1; then
		printf '%s' "$1" | shasum -a 256 | awk '{print "sha256:" $1}'
		return
	fi
	echo 'SHA-256 digest utility is required' >&2
	return 1
}

amd64_raw="$(inspect_raw "$image:$tag-amd64")"
arm64_raw="$(inspect_raw "$image:$tag-arm64")"

for architecture in amd64 arm64; do
	case "$architecture" in
		amd64) raw="$amd64_raw" ;;
		arm64) raw="$arm64_raw" ;;
	esac
	if ! jq -e '
      .schemaVersion == 2
      and (.mediaType == "application/vnd.oci.image.manifest.v1+json"
        or .mediaType == "application/vnd.docker.distribution.manifest.v2+json")
      and (has("manifests") | not)
      and (.config.digest | test("^sha256:[0-9a-f]{64}$"))
      and (.layers | type == "array")
    ' >/dev/null <<<"$raw"; then
		echo "$image:$tag-$architecture is not a direct single-platform image manifest" >&2
		exit 1
	fi
done

amd64_digest="$(manifest_digest "$amd64_raw")"
arm64_digest="$(manifest_digest "$arm64_raw")"
# The index entry must repeat the media type the architecture manifest actually
# declares. Hardcoding the OCI type here while the per-architecture check above
# accepted an OCI *or* a Docker v2 manifest meant a legitimate Docker v2 build
# passed the first stage and failed only the second, with a message about the
# index that named neither the media type nor the architecture responsible.
amd64_media_type="$(jq -r '.mediaType' <<<"$amd64_raw")"
arm64_media_type="$(jq -r '.mediaType' <<<"$arm64_raw")"
index_raw="$(inspect_raw "$image:$tag")"

if ! jq -e --arg amd64 "$amd64_digest" --arg arm64 "$arm64_digest" \
	--arg amd64_media_type "$amd64_media_type" --arg arm64_media_type "$arm64_media_type" '
    .schemaVersion == 2
    and (.mediaType == "application/vnd.oci.image.index.v1+json"
      or .mediaType == "application/vnd.docker.distribution.manifest.list.v2+json")
    and (.manifests | length == 2)
    and ([.manifests[] | {
      digest,
      mediaType,
      os: .platform.os,
      architecture: .platform.architecture,
      variant: (.platform.variant // "")
    }] | sort_by(.architecture)) == [
      {
        digest: $amd64,
        mediaType: $amd64_media_type,
        os: "linux",
        architecture: "amd64",
        variant: ""
      },
      {
        digest: $arm64,
        mediaType: $arm64_media_type,
        os: "linux",
        architecture: "arm64",
        variant: ""
      }
    ]
  ' >/dev/null <<<"$index_raw"; then
	echo "$image:$tag is not exactly the direct Linux amd64 and Linux arm64 image index" >&2
	echo "expected amd64 $amd64_digest ($amd64_media_type) and arm64 $arm64_digest ($arm64_media_type); the index declares:" >&2
	jq -r '"    mediaType \(.mediaType)", (.manifests // [] | .[] | "    \(.platform.os // "?")/\(.platform.architecture // "?")\(if .platform.variant then "/" + .platform.variant else "" end) \(.digest) \(.mediaType)")' <<<"$index_raw" >&2
	exit 1
fi

echo "verified $image:$tag and direct $tag-amd64/$tag-arm64 images"
