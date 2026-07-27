#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow="$root/.github/workflows/publish-container.yml"
gha='$'
continuation="\\"

# Leading indentation is deliberately not part of the contract. It used to be:
# every assertion was an exact-line match including its exact column, so a YAML
# reindent failed the publication gate for a formatting reason while changing
# nothing the contract is about. The line's content is still matched exactly.
trim() {
	local value="$1"
	printf '%s' "${value#"${value%%[![:space:]]*}"}"
}

# One scratch directory and one trap: three separate `trap ... EXIT` lines used
# to overwrite one another, so only the last one's files were ever removed.
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT INT TERM
undented="$scratch/publish-container.undented"
sed 's/^[[:space:]]*//' "$workflow" >"$undented"

expect_count() {
	local expected="$1" literal actual
	literal="$(trim "$2")"
	actual="$(grep -Fxc -- "$literal" "$undented" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "publication workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

# specs/dependency-policy.md forbids floating tags: `runs-on: ubuntu-latest` is
# one, and this gate used to assert its presence, so the gate enforced the
# violation. Every runner label is now an exact image version, and
# tests/dependency-admission rejects any `-latest` runner in either workflow.
expect_count 1 'runner: ubuntu-24.04'
expect_count 1 'runner: ubuntu-24.04-arm'
expect_count 1 '- platform: linux/amd64'
expect_count 1 '- platform: linux/arm64'
expect_count 1 'IMAGE: ghcr.io/e6qu/someoldchat'
expect_count 3 "run: echo \"short_sha=${gha}{GITHUB_SHA:0:12}\" >> \"${gha}GITHUB_OUTPUT\""
expect_count 1 "tags: ${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-${gha}{{ matrix.arch.suffix }}"
expect_count 2 "build-args: RELEASE_REVISION=${gha}{{ github.sha }}"
expect_count 2 'provenance: false'
expect_count 1 'sbom: false'
expect_count 1 'sbom: true'
expect_count 2 'uses: actions/attest@f7c74d28b9d84cb8768d0b8ca14a4bac6ef463e6 # v4.2.0'
expect_count 2 "subject-digest: ${gha}{{ steps.push.outputs.digest }}"
expect_count 2 'create-storage-record: false'
expect_count 1 "sbom-path: ${gha}{{ runner.temp }}/someoldchat-${gha}{{ matrix.arch.suffix }}.spdx.json"
expect_count 1 "./scripts/extract-buildkit-sbom.sh ${continuation}"
expect_count 1 "--tag \"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}\" ${continuation}"
expect_count 1 "\"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-amd64\" ${continuation}"
expect_count 1 "\"${gha}{{ env.IMAGE }}:${gha}{{ steps.version.outputs.short_sha }}-arm64\""
expect_count 1 "run: ./scripts/verify-published-container.sh \"${gha}{{ env.IMAGE }}\" \"${gha}{{ steps.version.outputs.short_sha }}\""
expect_count 1 'name: Retain 20 release groups'
expect_count 1 './scripts/prune-ghcr-images.sh'
expect_count 1 '20'

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

expect_count 1 'push: true'
expect_count 1 'push: false'

fixture="$scratch/versions.json"
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

incomplete_fixture="$scratch/versions-incomplete.json"
jq --arg tag "$current_tag-arm64" 'map(select((.metadata.container.tags // []) != [$tag]))' "$fixture" >"$incomplete_fixture"
missing="$(jq -r --arg root "$current_tag" -f "$root/scripts/missing-current-container-tags.jq" "$incomplete_fixture")"
if [[ "$missing" != "$current_tag-arm64: expected one singleton package version, observed 0" ]]; then
	echo "incomplete current release produced unexpected visibility result: $missing" >&2
	exit 1
fi

# The first publication of a new package sees an empty version list. `jq -s add`
# yields null there, and every consumer then iterates $versions[] on null and
# dies with a jq type error, so the very first retention run of a new package
# failed. Both filters must survive the empty list.
empty_fixture="$scratch/versions-empty.json"
printf '[]\n' >"$empty_fixture"
if ! jq -r --argjson keep 20 -f "$root/scripts/select-obsolete-container-versions.jq" "$empty_fixture" >/dev/null; then
	echo 'retention selector fails on the empty version list of a first publication' >&2
	exit 1
fi
empty_missing="$(jq -r --arg root "$current_tag" -f "$root/scripts/missing-current-container-tags.jq" "$empty_fixture")"
if [[ "$empty_missing" != "$current_tag: expected one singleton package version, observed 0
$current_tag-amd64: expected one singleton package version, observed 0
$current_tag-arm64: expected one singleton package version, observed 0" ]]; then
	echo "empty version list produced unexpected visibility result: $empty_missing" >&2
	exit 1
fi
if [[ "$(printf '' | jq -s -c 'add // []')" != '[]' ]]; then
	echo 'retention must fold an empty version stream to an empty list, not null' >&2
	exit 1
fi
retention_folds="$(sed 's/#.*//' "$root/scripts/prune-ghcr-images.sh" | grep -c "jq -s 'add // \[\]'" || true)"
retention_bare_folds="$(sed 's/#.*//' "$root/scripts/prune-ghcr-images.sh" | grep -c "jq -s 'add'" || true)"
if [[ "$retention_folds" != 2 || "$retention_bare_folds" != 0 ]]; then
	echo "retention must fold both version listings with jq -s 'add // []' so a first publication does not fail on a null version list" >&2
	exit 1
fi

# A release version carrying a second tag is classified unclassifiable and
# therefore selected for deletion. That is the right classification, but it must
# never be applied to the release under publication: aliasing the current release
# made the release being published deletable. prune-ghcr-images.sh refuses before
# it deletes anything.
if ! grep -Fq 'refusing to delete anything' "$root/scripts/prune-ghcr-images.sh"; then
	echo 'retention must refuse to delete a package version carrying a tag of the release under publication' >&2
	exit 1
fi
aliased_fixture="$scratch/versions-aliased.json"
jq --arg tag "$current_tag" 'map(if (.metadata.container.tags // []) == [$tag] then .metadata.container.tags += ["release"] else . end)' "$fixture" >"$aliased_fixture"
aliased_selected="$(jq -r --argjson keep 20 -f "$root/scripts/select-obsolete-container-versions.jq" "$aliased_fixture" | sort -n | paste -sd, -)"
if [[ ",$aliased_selected," != *",210,"* ]]; then
	echo "an aliased current release was expected to be classified obsolete so the refusal above can catch it; selector chose: $aliased_selected" >&2
	exit 1
fi

# Matched without their indentation for the same reason as the workflow
# assertions: a reindent of the retention script is not a contract change.
if [[ "$(grep -Ec '^[[:space:]]*visibility_attempts=12$' "$root/scripts/prune-ghcr-images.sh")" != 1 ]] ||
	[[ "$(grep -Ec '^[[:space:]]*sleep 5$' "$root/scripts/prune-ghcr-images.sh")" != 1 ]]; then
	echo 'retention must wait for GitHub Packages metadata to expose the published release' >&2
	exit 1
fi

if grep -Eq 'predicate-(type|path):' "$workflow"; then
	echo 'publication provenance must use the official action native SLSA generator' >&2
	exit 1
fi

# No container may be built from a commit the repository gates have not run
# against. Before this check the publication workflow ran only
# check-container-publication.sh and then pushed an image, while the pull-request
# workflow never triggered on `push`, so any commit reaching the default branch
# outside a pull request shipped with no formatting, vet, dependency, contract,
# or test evidence at all.
integration="$root/.github/workflows/ci.yml"

expect_count 1 '    needs: [contract, verify]'
expect_count 1 '    uses: ./.github/workflows/ci.yml'
expect_count 1 '  cancel-in-progress: false'

for trigger in 'push:' 'branches: [main]' 'workflow_call:'; do
	if ! grep -Fq -- "$trigger" "$integration"; then
		echo "ci.yml must declare '$trigger' so the publication path verifies the pushed commit" >&2
		exit 1
	fi
done

# The gate set is compared name by name, not by substring. `grep -Fq "test"`
# matched `make test-load`, so the entire unit-test step could be deleted and a
# container still published; and the list omitted module-docs-check,
# module-startup-check, task-flags-check, vuln-check, test-race, test-load,
# test-fuzz and build-static, every one of which could be deleted from ci.yml
# with this contract still passing. Both were proved by deleting them.
#
# Every target named on a `make` command line anywhere in ci.yml is collected,
# including the ones inside `run: |` blocks, and matched exactly. A target must
# therefore be present as its own word, so `test` is no longer satisfied by
# `test-load`. Trailing VARIABLE=value arguments are uppercase and stop the
# match, so `make contract-ratchet BASE_REF=...` contributes `contract-ratchet`.
integration_gates="$(grep -oE '\bmake +[a-z][a-z0-9 -]*' "$integration" | sed -E 's/^make +//' | tr ' ' '\n' | grep -v '^$' | sort -u)"
for gate in \
	fmt-check vet workflow-check container-check module-docs-check module-startup-check task-flags-check \
	dependency-check contract-check sdk-inventory-check generated-check proto-lint vuln-check \
	activator-check test test-race test-load test-transport-load test-fuzz build-static; do
	if ! printf '%s\n' "$integration_gates" | grep -Fxq -- "$gate"; then
		echo "ci.yml must run 'make $gate' before a commit can be published" >&2
		exit 1
	fi
done

"$root/scripts/test-extract-buildkit-sbom.sh"

echo 'container publication contract passed'
