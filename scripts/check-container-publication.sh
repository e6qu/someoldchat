#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow="$root/.github/workflows/publish-container.yml"
gha='$'
continuation="\\"

expect_count() {
	local expected="$1" literal="$2" actual
	actual="$(grep -Fxc -- "$literal" "$workflow" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "publication workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

expect_count 1 '            runner: ubuntu-latest'
expect_count 1 '            runner: ubuntu-24.04-arm'
expect_count 1 '          - platform: linux/amd64'
expect_count 1 '          - platform: linux/arm64'
expect_count 1 '  IMAGE: ghcr.io/e6qu/someoldchat'
expect_count 3 "        run: echo \"short_sha=${gha}{GITHUB_SHA:0:12}\" >> \"${gha}GITHUB_OUTPUT\""
expect_count 1 "          tags: ${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-${gha}{{ matrix.arch.suffix }}"
expect_count 2 "          build-args: RELEASE_REVISION=${gha}{{ github.sha }}"
expect_count 2 '          provenance: false'
expect_count 1 '          sbom: false'
expect_count 1 '          sbom: true'
expect_count 2 '        uses: actions/attest@f7c74d28b9d84cb8768d0b8ca14a4bac6ef463e6 # v4.2.0'
expect_count 2 "          subject-digest: ${gha}{{ steps.push.outputs.digest }}"
expect_count 2 '          create-storage-record: false'
expect_count 1 "          sbom-path: ${gha}{{ runner.temp }}/someoldchat-${gha}{{ matrix.arch.suffix }}.spdx.json"
expect_count 1 "          ./scripts/extract-buildkit-sbom.sh ${continuation}"
expect_count 1 "            --tag \"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}\" ${continuation}"
expect_count 1 "            \"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-amd64\" ${continuation}"
expect_count 1 "            \"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-arm64\""
expect_count 1 "        run: ./scripts/verify-published-container.sh \"${gha}{{ env.IMAGE }}\" \"${gha}{{ steps.version.outputs.short_sha }}\""
expect_count 1 '    name: Retain 20 release groups'
expect_count 1 '          ./scripts/prune-ghcr-images.sh'
expect_count 1 '          20'

if grep -E '(tags:|--tag)[^#]*:(latest|main)([^[:alnum:]_-]|$)' "$workflow"; then
	echo 'publication workflow must not publish latest or main image tags' >&2
	exit 1
fi

if grep -Fq 'outputs.sha' "$workflow" || grep -Fq "sha=${gha}{GITHUB_SHA}" "$workflow"; then
	echo 'publication workflow must not publish full-commit-SHA image tags' >&2
	exit 1
fi

tag_destination_count="$(grep -Ec '^[[:space:]]+(tags:|--tag )' "$workflow")"
if [[ "$tag_destination_count" != 2 ]]; then
	echo "publication workflow declared $tag_destination_count tag destinations; expected one architecture template and one generic index" >&2
	exit 1
fi
if grep -E '(tags:|--tag).*GITHUB_SHA' "$workflow"; then
	echo 'publication tags must use the validated 12-character commit output' >&2
	exit 1
fi

expect_count 1 '          push: true'
expect_count 1 '          push: false'

fixture="$(mktemp)"
trap 'rm -f "$fixture"' EXIT
jq -n '[
  range(0; 22) as $release
  | (("000000000000" + ($release | tostring))[-12:]) as $tag
  | range(0; 3) as $kind
  | {
      id: ($release * 10 + $kind),
      created_at: ("2026-07-" + (("00" + (($release + 1) | tostring))[-2:]) + "T00:00:00Z"),
      metadata: {container: {tags: [
        if $kind == 0 then $tag
        elif $kind == 1 then ($tag + "-amd64")
        else ($tag + "-arm64") end
      ]}}
    }
] + [
  {id: 999, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["main"]}}},
  {id: 1000, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["0123456789abcdef0123456789abcdef01234567"]}}},
  {id: 1001, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: []}}},
  {id: 1002, created_at: "2026-08-03T00:00:00Z", metadata: {container: {tags: ["ffffffffffff"]}}},
  {id: 1003, created_at: "2026-08-03T00:00:01Z", metadata: {container: {tags: ["ffffffffffff-amd64"]}}},
  {id: 1004, created_at: "2026-08-02T00:00:00Z", metadata: {container: {tags: ["eeeeeeeeeeee"]}}},
  {id: 1005, created_at: "2026-08-02T00:00:01Z", metadata: {container: {tags: ["eeeeeeeeeeee-amd64"]}}},
  {id: 1006, created_at: "2026-08-02T00:00:02Z", metadata: {container: {tags: ["eeeeeeeeeeee-arm64", "main"]}}}
]' >"$fixture"

selected="$(jq -r --argjson keep 20 -f "$root/scripts/select-obsolete-container-versions.jq" "$fixture" | sort -n | paste -sd, -)"
if [[ "$selected" != '0,1,2,10,11,12,999,1000,1001,1002,1003,1004,1005,1006' ]]; then
	echo "retention selector chose unexpected package versions: $selected" >&2
	exit 1
fi

current_tag=000000000021
missing="$(jq -r --arg root "$current_tag" -f "$root/scripts/missing-current-container-tags.jq" "$fixture")"
if [[ -n "$missing" ]]; then
	echo "complete current release was reported missing: $missing" >&2
	exit 1
fi

incomplete_fixture="$(mktemp)"
trap 'rm -f "$fixture" "$incomplete_fixture"' EXIT
jq --arg tag "$current_tag-arm64" 'map(select((.metadata.container.tags // []) != [$tag]))' "$fixture" >"$incomplete_fixture"
missing="$(jq -r --arg root "$current_tag" -f "$root/scripts/missing-current-container-tags.jq" "$incomplete_fixture")"
if [[ "$missing" != "$current_tag-arm64: expected one singleton package version, observed 0" ]]; then
	echo "incomplete current release produced unexpected visibility result: $missing" >&2
	exit 1
fi

if [[ "$(grep -Fxc 'visibility_attempts=12' "$root/scripts/prune-ghcr-images.sh")" != 1 ]] ||
	[[ "$(grep -Fxc $'\tsleep 5' "$root/scripts/prune-ghcr-images.sh")" != 1 ]]; then
	echo 'retention must wait for GitHub Packages metadata to expose the published release' >&2
	exit 1
fi

if grep -Eq 'predicate-(type|path):' "$workflow"; then
	echo 'publication provenance must use the official action native SLSA generator' >&2
	exit 1
fi

"$root/scripts/test-extract-buildkit-sbom.sh"

echo 'container publication contract passed'
