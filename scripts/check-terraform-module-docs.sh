#!/usr/bin/env bash
# Asserts that each Terraform module's README example is a call `terraform plan`
# would accept: it supplies every variable that has no default, names no variable
# the module does not declare, and points its `source` at the module itself.
#
# `deploy/ecs-scale-zero/README.md`'s example omitted seven `websocket_*`
# variables, so the documented `terraform plan` failed outright with "No value
# for required variable" — a defect no gate could see, because `terraform
# validate` never reads the README and `terraform plan` needs credentials.
#
# The converse direction went unchecked for a whole release: an example that
# passes a variable the module does not declare fails `terraform plan` with "An
# argument named ... is not expected here", which is the same class of defect,
# and this script passed. The module list was also hardcoded, so a third module
# with a broken README was invisible; modules are now discovered, which is what
# the sibling check-task-definition-flags.sh means by refusing to let a new
# definition opt out by omission.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"
status=0

# Emits the name of every variable a module declares, and "<name> default" for
# each one that declares a default.
declared_variables() {
	local directory="$1"
	awk '
		/^variable "/ {
			name = $2
			gsub(/"/, "", name)
			depth = 0
			has_default = 0
		}
		name != "" && /^[[:space:]]*default[[:space:]]*=/ { has_default = 1 }
		name != "" {
			for (i = 1; i <= length($0); i++) {
				character = substr($0, i, 1)
				if (character == "{") depth++
				if (character == "}") {
					depth--
					if (depth == 0) {
						print name (has_default ? " default" : " required")
						name = ""
						break
					}
				}
			}
		}
	' "$directory"/*.tf | sort -u
}

# Emits every argument name the README example passes to the module block, and
# the block's `source` as "source <value>".
#
# Scoped to the `module "..."` block: the deploy example also contains a
# `provider "aws" { region = ... }`, and matching the whole fenced block let an
# argument in an unrelated block satisfy a required module variable.
example_arguments() {
	awk '
		/^module[[:space:]]+"/ { inside = 1; depth = 0 }
		inside {
			opened = 0
			closed = 0
			for (i = 1; i <= length($0); i++) {
				character = substr($0, i, 1)
				if (character == "{") { depth++; opened++ }
				if (character == "}") { depth--; closed++ }
			}
			if (depth == 1 && opened <= 1 && match($0, /^[[:space:]]*[A-Za-z_][A-Za-z0-9_-]*[[:space:]]*=/)) {
				name = substr($0, RSTART, RLENGTH)
				sub(/^[[:space:]]*/, "", name)
				sub(/[[:space:]]*=$/, "", name)
				if (name == "source") {
					value = $0
					sub(/^[^=]*=[[:space:]]*/, "", value)
					gsub(/"/, "", value)
					sub(/[[:space:]]*$/, "", value)
					print "source " value
				} else {
					print name
				}
			}
			if (depth <= 0 && closed > 0) inside = 0
		}
	'
}

# Every directory holding Terraform that declares variables is a module this
# check covers. The list used to be two hardcoded paths, so a third module was
# not covered at all.
modules() {
	find deploy terraform -name '*.tf' -not -path '*/.terraform/*' -print0 |
		xargs -0 -n1 dirname | sort -u |
		while IFS= read -r directory; do
			if grep -lq '^variable "' "$directory"/*.tf 2>/dev/null; then
				printf '%s\n' "$directory"
			fi
		done
}

module_list="$(modules)"
if [[ -z "$module_list" ]]; then
	echo 'no Terraform module with declared variables was found; this check would silently cover nothing' >&2
	exit 1
fi

while IFS= read -r module; do
	[[ -n "$module" ]] || continue
	readme="$root/$module/README.md"
	if [[ ! -f "$readme" ]]; then
		echo "$module has no README.md; a shipped module must be documented" >&2
		status=1
		continue
	fi
	# Only the first fenced hcl block. `sed -n '/^```hcl$/,/^```$/p'`
	# concatenated every fenced block in the README, so an unrelated second
	# example could supply a required variable the documented example omits —
	# proved by moving `websocket_certificate_arn` into a second block, which
	# passed while `terraform plan` on the documented call still failed with "No
	# value for required variable".
	# shellcheck disable=SC2016 # the backticks are a literal fenced-block marker
	example="$(awk '/^```hcl$/ { if (seen) exit; seen = 1; next } /^```$/ { if (seen) exit } seen' "$readme")"
	if [[ -z "$example" ]]; then
		echo "$module/README.md has no hcl example, so its required variables cannot be verified" >&2
		status=1
		continue
	fi
	arguments="$(printf '%s\n' "$example" | example_arguments)"
	if [[ -z "$arguments" ]]; then
		echo "$module/README.md has no 'module \"...\" { ... }' block in its hcl example, so the example cannot be verified" >&2
		status=1
		continue
	fi
	declared="$(declared_variables "$root/$module")"

	# The example is written for a root module at the repository root, so its
	# source is the module's own repository-relative path. An example calling a
	# path that does not exist documents a `terraform init` that cannot succeed.
	source_value="$(printf '%s\n' "$arguments" | awk '$1 == "source" { print $2; exit }')"
	if [[ -z "$source_value" ]]; then
		echo "$module/README.md example declares no source, so a reader cannot tell which module it calls" >&2
		status=1
	elif [[ "${source_value#./}" != "$module" ]]; then
		echo "$module/README.md example source is '$source_value', which does not resolve to the module at './$module'" >&2
		status=1
	fi

	while IFS= read -r variable; do
		[[ -n "$variable" ]] || continue
		if ! printf '%s\n' "$arguments" | grep -Fxq -- "$variable"; then
			echo "$module/README.md example omits required variable '$variable'" >&2
			status=1
		fi
	done <<<"$(printf '%s\n' "$declared" | awk '$2 == "required" { print $1 }')"

	while IFS= read -r argument; do
		[[ -n "$argument" ]] || continue
		[[ "$argument" != source* ]] || continue
		if ! printf '%s\n' "$declared" | awk '{ print $1 }' | grep -Fxq -- "$argument"; then
			echo "$module/README.md example passes '$argument', which $module declares no variable for; terraform plan fails with \"An argument named \\\"$argument\\\" is not expected here\"" >&2
			status=1
		fi
	done <<<"$arguments"
done <<<"$module_list"

if [[ "$status" -ne 0 ]]; then
	exit 1
fi
echo "terraform module documentation contract passed for $(printf '%s\n' "$module_list" | paste -sd' ' -)"
