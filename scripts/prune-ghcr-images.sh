#!/usr/bin/env bash
set -euo pipefail

owner="${1:?usage: prune-ghcr-images.sh <owner> <package> <current-tag> [release-count]}"
package="${2:?usage: prune-ghcr-images.sh <owner> <package> <current-tag> [release-count]}"
current_tag="${3:?usage: prune-ghcr-images.sh <owner> <package> <current-tag> [release-count]}"
keep="${4:-20}"

if [[ ! "$current_tag" =~ ^[0-9a-f]{12}$ ]]; then
	echo "current tag must be a lowercase 12-character commit SHA: $current_tag" >&2
	exit 1
fi
if [[ ! "$keep" =~ ^[1-9][0-9]*$ ]]; then
	echo "release count must be a positive integer: $keep" >&2
	exit 1
fi

case "$(gh api "/users/$owner" --jq .type)" in
	Organization) package_namespace=orgs ;;
	User) package_namespace=users ;;
	*)
		echo "unsupported GitHub package owner: $owner" >&2
		exit 1
		;;
esac

base="/$package_namespace/$owner/packages/container/$package/versions"
versions_file="$(mktemp)"
remaining_file="$(mktemp)"
trap 'rm -f "$versions_file" "$remaining_file"' EXIT

gh api --paginate "$base?per_page=100" | jq -s 'add' >"$versions_file"
jq -r --argjson keep "$keep" -f "$(dirname "${BASH_SOURCE[0]}")/select-obsolete-container-versions.jq" "$versions_file" |
	while IFS= read -r version_id; do
		echo "deleting obsolete $package package version $version_id"
		gh api --method DELETE "$base/$version_id"
	done

gh api --paginate "$base?per_page=100" | jq -s 'add' >"$remaining_file"

release_count="$(jq '[.[].metadata.container.tags[]? | select(test("^[0-9a-f]{12}$"))] | unique | length' "$remaining_file")"
if ((release_count > keep)); then
	echo "$package retained $release_count release groups; expected at most $keep" >&2
	exit 1
fi

version_count="$(jq 'length' "$remaining_file")"
if ((version_count > keep * 3)); then
	echo "$package retained $version_count package versions; expected at most $((keep * 3))" >&2
	exit 1
fi

invalid_tags="$(jq -r '
  [.[].metadata.container.tags[]? | select(test("^[0-9a-f]{12}$"))] as $roots
  | ($roots | map(., . + "-amd64", . + "-arm64") | unique) as $allowed
  | [.[].metadata.container.tags[]? | select(. as $tag | $allowed | index($tag) == null)]
  | unique
  | .[]
' "$remaining_file")"
if [[ -n "$invalid_tags" ]]; then
	echo 'unexpected tags remained after GitHub Container Registry retention:' >&2
	printf '%s\n' "$invalid_tags" >&2
	exit 1
fi

for expected_tag in "$current_tag" "$current_tag-amd64" "$current_tag-arm64"; do
	occurrences="$(jq --arg tag "$expected_tag" '[.[].metadata.container.tags[]? | select(. == $tag)] | length' "$remaining_file")"
	if [[ "$occurrences" != 1 ]]; then
		echo "$package retained $occurrences copies of current tag $expected_tag; expected exactly one" >&2
		exit 1
	fi
done

echo "$package retained $release_count valid immutable release group(s) in $version_count package version(s)"
