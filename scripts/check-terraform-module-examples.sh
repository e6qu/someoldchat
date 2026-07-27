#!/usr/bin/env bash
# Runs each Terraform module's README example through `terraform validate`.
#
# scripts/check-terraform-module-docs.sh compares argument *names* — every
# no-default variable is supplied, no undeclared variable is passed, the source
# resolves. That is everything a name comparison can decide, and it left two
# whole classes of broken example passing, both proved by injection:
#
#   - the module's own `validation` blocks were never evaluated, so
#     deploy/ecs-scale-zero/README.md could document
#     `application_command = ["-store", "sqlite", ...]` — the one call the module
#     exists to forbid, because that tier is stopped with ecs:StopTask and a
#     task-local store is unrecoverable data loss — and the gate passed;
#   - types were never checked, so `private_subnet_ids = 12345` against a
#     `list(string)` passed.
#
# Both are decided by `terraform validate` against a root module built from the
# example, which is why this is a separate gate from the name comparison: it
# needs the provider registry, and the workflow job that runs the name
# comparison has no Terraform at all.
#
# Only the *first* fenced hcl block is taken. The name comparison used to
# concatenate every fenced block in the README, so an unrelated second example
# could satisfy the first one's required variables while the documented
# `terraform plan` still failed with "No value for required variable".
#
# An argument whose value is not a literal cannot be evaluated here, so it is
# replaced with a placeholder and the variable it referenced is declared with no
# value. Literal arguments — which is what a reader copies and what an injected
# defect looks like — reach the module unchanged and are validated for real.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT INT TERM
status=0

# A shared plugin cache means the provider is downloaded once for every module
# rather than once per module.
export TF_PLUGIN_CACHE_DIR="${TF_PLUGIN_CACHE_DIR:-$root/.cache/terraform-plugins}"
mkdir -p "$TF_PLUGIN_CACHE_DIR"

modules() {
	if [[ "$#" -gt 0 ]]; then
		printf '%s\n' "$@"
		return
	fi
	find deploy terraform -name '*.tf' -not -path '*/.terraform/*' -print0 |
		xargs -0 -n1 dirname | sort -u |
		while IFS= read -r directory; do
			if grep -lq '^variable "' "$directory"/*.tf 2>/dev/null; then
				printf '%s\n' "$directory"
			fi
		done
}

module_list="$(modules "$@")"
if [[ -z "$module_list" ]]; then
	echo 'no Terraform module with declared variables was found; this check would silently cover nothing' >&2
	exit 1
fi

covered=0
while IFS= read -r module; do
	[[ -n "$module" ]] || continue
	readme="$root/$module/README.md"
	if [[ ! -f "$readme" ]]; then
		echo "$module has no README.md; a shipped module must be documented" >&2
		status=1
		continue
	fi
	work="$scratch/$(printf '%s' "$module" | tr '/' '-')"
	mkdir -p "$work"
	# shellcheck disable=SC2016 # the backticks are a literal fenced-block marker
	if ! awk '/^```hcl$/ { if (seen) exit; seen = 1; next } /^```$/ { if (seen) exit } seen' "$readme" >"$work/example.hcl"; then
		echo "$module/README.md could not be read" >&2
		status=1
		continue
	fi
	if [[ ! -s "$work/example.hcl" ]]; then
		echo "$module/README.md has no hcl example, so it cannot be validated" >&2
		status=1
		continue
	fi
	awk -v source="$root/$module" '
		BEGIN { depth = 0 }
		{
			line = $0
			if (line ~ /^module[[:space:]]+"/) { inside = 1 }
			if (inside && depth == 1 && match(line, /^[[:space:]]*[A-Za-z_][A-Za-z0-9_-]*[[:space:]]*=/)) {
				name = substr(line, RSTART, RLENGTH)
				sub(/^[[:space:]]*/, "", name)
				sub(/[[:space:]]*=$/, "", name)
				value = line
				sub(/^[^=]*=[[:space:]]*/, "", value)
				sub(/[[:space:]]*$/, "", value)
				if (name == "source") {
					line = "  source = \"" source "\""
				} else if (value ~ /^var\./) {
					declared[value] = 1
				} else if (value !~ /^["[{0-9]/ && value != "true" && value != "false") {
					# A resource, data, local, or module reference. It cannot be
					# resolved here, and its value is not what a reader copies.
					line = "  " name " = \"example-placeholder\""
				}
			}
			for (i = 1; i <= length($0); i++) {
				character = substr($0, i, 1)
				if (character == "{") depth++
				else if (character == "}") { depth--; if (depth <= 0) inside = 0 }
			}
			print line
		}
		END {
			for (reference in declared) {
				name = reference
				sub(/^var\./, "", name)
				sub(/\..*$/, "", name)
				wanted[name] = 1
			}
			for (name in wanted) { printf "variable \"%s\" {}\n", name }
		}
	' "$work/example.hcl" >"$work/main.tf"
	rm -f "$work/example.hcl"
	if ! output="$( (cd "$work" && terraform init -backend=false -input=false && terraform validate -no-color) 2>&1)"; then
		echo "$module/README.md documents a call terraform refuses:" >&2
		printf '%s\n' "$output" | sed 's/^/    /' >&2
		status=1
		continue
	fi
	covered=$((covered + 1))
	echo "$module: the README example is a call terraform validate accepts"
done <<<"$module_list"

if [[ "$covered" -eq 0 && "$status" -eq 0 ]]; then
	echo 'no module example was validated; this check would silently cover nothing' >&2
	exit 1
fi
if [[ "$status" -ne 0 ]]; then
	exit 1
fi
echo 'terraform module example contract passed'
