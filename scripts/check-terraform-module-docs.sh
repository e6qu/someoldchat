#!/usr/bin/env bash
# Asserts that each Terraform module's README example supplies every variable
# that has no default.
#
# `deploy/ecs-scale-zero/README.md`'s example omitted seven `websocket_*`
# variables, so the documented `terraform plan` failed outright with "No value
# for required variable" — a defect no gate could see, because `terraform
# validate` never reads the README and `terraform plan` needs credentials.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
status=0

# Emits the name of every variable in a module that declares no default.
required_variables() {
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
						if (!has_default) print name
						name = ""
						break
					}
				}
			}
		}
	' "$directory"/*.tf | sort -u
}

for module in deploy/ecs-scale-zero terraform/ecs-runtime; do
	readme="$root/$module/README.md"
	if [[ ! -f "$readme" ]]; then
		echo "$module has no README.md; a shipped module must be documented" >&2
		status=1
		continue
	fi
	# shellcheck disable=SC2016 # the backticks are a literal fenced-block marker
	example="$(sed -n '/^```hcl$/,/^```$/p' "$readme")"
	if [[ -z "$example" ]]; then
		echo "$module/README.md has no hcl example, so its required variables cannot be verified" >&2
		status=1
		continue
	fi
	while IFS= read -r variable; do
		[[ -n "$variable" ]] || continue
		if ! printf '%s\n' "$example" | grep -Eq "(^|[[:space:]])${variable}[[:space:]]*="; then
			echo "$module/README.md example omits required variable '$variable'" >&2
			status=1
		fi
	done <<<"$(required_variables "$root/$module")"
done

if [[ "$status" -ne 0 ]]; then
	exit 1
fi
echo 'terraform module documentation contract passed'
