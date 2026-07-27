#!/usr/bin/env bash
# Cross-references the command-line flags a Terraform container definition passes
# to a repository binary against the flags that binary actually defines.
#
# `deploy/ecs-scale-zero/main.tf` passed `-subnets`, `-security-groups`, and
# `-replicas` to `cmd/ecs-ws-activator` after that binary stopped accepting them,
# and omitted `-activator-url`, `-activator-token`, and `-idle-timeout`, which it
# requires. Every gate passed: `terraform validate` type-checks a container
# command as an opaque list of strings, and no Go test reads a `.tf` file. The
# deployed edge task would have exited 2 on every start.
#
# The Terraform side of the contract is declared with a comment inside the
# resource:
#
#     # flag-contract: cmd/ecs-ws-activator
#     command = [ "-listen", ":8080", ... ]
#
# A command whose value is not a literal list — `command = var.application_command`
# — cannot be checked against any binary, so it must say so:
#
#     # flag-contract: caller-supplied
#     command = var.application_command
#
# The Go side is the binary's own `flag` usage output, which is authoritative for
# every binding style (`flag.String`, `flag.XxxVar`, a `FlagSet` bound by another
# type) and cannot drift from the parsed flag set. A flag whose usage text ends in
# "(required)" must appear in the command; any flag in the command must be
# defined.
#
# Every container command in every module must carry one of the two annotations,
# so a new task definition cannot opt out of the check by omission. That claim
# used to be false five ways, all proved: `-flag=value` matched no token at all,
# so `"-bogus-flag=1"` passed and the task exited 2 at start; the same form was
# reported as an *omission* when written legitimately as `"-listen=:8080"`; a
# command supplied through a variable produced neither tokens nor an annotation
# error, so the application server's flags were never checked at all; a command
# written as a JSON key inside a `container_definitions` heredoc —
# `[{"name":"probe","command":["-bogus-flag","1"]}]` — matched neither the
# attribute pattern nor anything else, so it produced no record and no
# annotation error; and `entryPoint`, which ECS prepends to `command`, was never
# inspected at all, so a bogus flag there exited 2 on every start with the gate
# green.
#
# A binary this repository ships but no module deploys is covered through its
# documentation instead. `cmd/activator` is deployed separately by the operator,
# so no `.tf` file names it and it was checked by nothing; the document that
# tells the operator which flags to pass now carries the same contract:
#
#     <!-- flag-contract: cmd/activator -->
#     ... prose naming every `-flag` ...
#     <!-- /flag-contract -->
#
# Inside such a region every backticked `-flag` must be one the binary defines,
# and every flag whose usage ends in "(required)" must be named. A document that
# tells an operator to pass a flag the binary dropped is the same defect as a
# task definition that does.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

: "${GOCACHE:=$root/.cache/go-build}"
export GOCACHE
export GOWORK=off

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT INT TERM
status=0

# Emits one "<file>:<line> <owner> <flag>" record per literal flag token found in
# a container command, and one "<file>:<line> <owner> !command" record per command
# block, so a command that produces no tokens is still visible. The owner is "-"
# when the block carries no `# flag-contract:` annotation. The annotation is
# scoped to the resource block it appears in so it cannot leak into an unrelated
# task definition.
#
# The token pattern accepts `-name` and `-name=value` and does not assume a
# lowercase first character, because Go's flag package accepts all of them and
# the deployed task is parsed by Go, not by this pattern.
extract_command_flags() {
	awk '
		/^[[:space:]]*(resource|data|module)[[:space:]]+"/ { contract = "" }
		/#[[:space:]]*flag-contract:/ {
			line = $0
			sub(/^.*#[[:space:]]*flag-contract:[[:space:]]*/, "", line)
			sub(/[[:space:]].*$/, "", line)
			contract = line
		}
		{
			# `command =` is matched anywhere on the line, not only at its start:
			# aws_ecs_task_definition.application writes its whole container
			# definition on one jsonencode line, so an anchored pattern never saw
			# the application server command at all. `entryPoint` is matched for
			# the same reason it exists in ECS — it is prepended to `command`, so
			# a flag there reaches the binary exactly as one in `command` does —
			# and both are matched as JSON keys as well as HCL attributes,
			# because `container_definitions` is just as often written as a JSON
			# heredoc, which produced no record at all.
			if (!collecting && ($0 ~ /(^|[[:space:]{,])(command|entryPoint)[[:space:]]*=/ || $0 ~ /"(command|entryPoint)"[[:space:]]*:/)) {
				collecting = 1
				depth = 0
				start = FNR
				owner = contract
				printf "%s:%d %s !command\n", FILENAME, start, (owner == "" ? "-" : owner)
			}
			if (!collecting) next
			rest = $0
			while (match(rest, /"-[A-Za-z0-9][A-Za-z0-9_-]*(=[^"]*)?"/)) {
				token = substr(rest, RSTART + 1, RLENGTH - 2)
				sub(/=.*$/, "", token)
				printf "%s:%d %s %s\n", FILENAME, start, (owner == "" ? "-" : owner), token
				rest = substr(rest, RSTART + RLENGTH)
			}
			opened = 0
			for (i = 1; i <= length($0); i++) {
				character = substr($0, i, 1)
				if (character == "[") { depth++; opened++ }
				else if (character == "]") depth--
			}
			# A single-line `command = var.x` opens no bracket at all and must not
			# swallow the rest of the file.
			if (depth <= 0) collecting = 0
		}
	' "$1"
}

# Emits the same record shape for a documented flag contract, so both sources are
# verified by one loop. A region with no flags in it still emits its !command
# record, so an emptied region is an error rather than a silent skip.
extract_documented_flags() {
	awk '
		/<!--[[:space:]]*flag-contract:[[:space:]]*/ {
			line = $0
			sub(/^.*<!--[[:space:]]*flag-contract:[[:space:]]*/, "", line)
			sub(/[[:space:]].*$/, "", line)
			sub(/-->.*$/, "", line)
			owner = line
			inside = 1
			printf "%s:%d %s !command\n", FILENAME, FNR, (owner == "" ? "-" : owner)
			next
		}
		/<!--[[:space:]]*\/flag-contract[[:space:]]*-->/ { inside = 0; owner = ""; next }
		inside {
			rest = $0
			while (match(rest, /`-[A-Za-z0-9][A-Za-z0-9_-]*(=[^`]*)?`/)) {
				token = substr(rest, RSTART + 1, RLENGTH - 2)
				sub(/=.*$/, "", token)
				printf "%s:%d %s %s\n", FILENAME, FNR, (owner == "" ? "-" : owner), token
				rest = substr(rest, RSTART + RLENGTH)
			}
		}
	' "$1"
}

terraform_files() {
	find deploy terraform -name '*.tf' -not -path '*/.terraform/*' | sort
}

documentation_files() {
	find docs deploy terraform -name '*.md' -not -path '*/node_modules/*' | sort
}

while IFS= read -r file; do
	extract_command_flags "$file" >>"$scratch/used"
done <<<"$(terraform_files)"
while IFS= read -r file; do
	extract_documented_flags "$file" >>"$scratch/used"
done <<<"$(documentation_files)"
touch "$scratch/used"

while read -r position package flag; do
	[[ -n "${position:-}" ]] || continue
	if [[ "$package" == "-" ]]; then
		if [[ "$flag" == "!command" ]]; then
			echo "$position declares a command with no 'flag-contract: cmd/<binary>' or 'flag-contract: caller-supplied' annotation, so it cannot be verified against any binary" >&2
		else
			echo "$position passes flag '$flag' with no 'flag-contract: cmd/<binary>' annotation, so it cannot be verified against any binary" >&2
		fi
		status=1
		continue
	fi
	if [[ "$package" == "caller-supplied" ]]; then
		if [[ "$flag" != "!command" ]]; then
			echo "$position is annotated 'caller-supplied' but passes the literal flag '$flag', which is verifiable; name the binary instead" >&2
			status=1
		fi
		continue
	fi
	[[ "$flag" != "!command" ]] || continue
	printf '%s\n' "$package" >>"$scratch/packages"
done <"$scratch/used"

if [[ ! -s "$scratch/packages" ]]; then
	echo 'no annotated container command or documented flag contract named a binary; this check would silently cover nothing' >&2
	exit 1
fi

sort -u "$scratch/packages" >"$scratch/packages.unique"

while IFS= read -r package; do
	[[ -n "$package" ]] || continue
	if [[ ! -d "$package" ]]; then
		echo "flag-contract names '$package', which is not a directory in this repository" >&2
		status=1
		continue
	fi
	# `flag` prints usage to stderr and exits 2 for -h; a build failure produces no
	# usage block at all, which must not be read as "this binary defines no flags".
	usage="$scratch/usage"
	go run "./$package" -h >"$usage" 2>&1 || true
	if ! grep -Eq '^[[:space:]]+-[A-Za-z0-9]' "$usage"; then
		echo "could not enumerate the flags of $package; 'go run ./$package -h' produced no flag usage:" >&2
		sed 's/^/    /' "$usage" >&2
		status=1
		continue
	fi
	awk '
		match($0, /^[[:space:]]+-[A-Za-z0-9][A-Za-z0-9_-]*/) {
			name = substr($0, RSTART, RLENGTH)
			sub(/^[[:space:]]+/, "", name)
			current = name
			print "defined " current
			next
		}
		# Only an unconditional "(required)" at the end of the usage text counts.
		# "(required for -snapshot-store=filesystem)" is a conditional requirement
		# this check cannot evaluate, so it must not be demanded of every caller.
		# Go appends its own " (default x)" after the usage text, which is not part
		# of the requirement and is stripped before the test.
		current != "" {
			text = $0
			sub(/[[:space:]]*\(default[[:space:]].*\)[[:space:]]*$/, "", text)
			if (text ~ /\(required\)[[:space:]]*$/) { print "required " current; current = "" }
		}
	' "$usage" | sed "s|^|$package |" >>"$scratch/declared"
done <"$scratch/packages.unique"
touch "$scratch/declared"

while IFS= read -r package; do
	[[ -n "$package" ]] || continue
	awk -v package="$package" '$2 == "defined" && $1 == package { print $3 }' "$scratch/declared" | sort -u >"$scratch/defined.list"
	awk -v package="$package" '$2 == "required" && $1 == package { print $3 }' "$scratch/declared" | sort -u >"$scratch/required.list"
	awk -v package="$package" '$2 == package && $3 != "!command" { print $3 }' "$scratch/used" | sort -u >"$scratch/used.list"
	[[ -s "$scratch/defined.list" ]] || continue

	while IFS= read -r flag; do
		[[ -n "$flag" ]] || continue
		if ! grep -Fxq -- "$flag" "$scratch/defined.list"; then
			position="$(awk -v package="$package" -v flag="$flag" '$2 == package && $3 == flag { print $1; exit }' "$scratch/used")"
			echo "$position passes '$flag' to $package, which does not define it" >&2
			status=1
		fi
	done <"$scratch/used.list"

	while IFS= read -r flag; do
		[[ -n "$flag" ]] || continue
		if ! grep -Fxq -- "$flag" "$scratch/used.list"; then
			position="$(awk -v package="$package" '$2 == package { print $1; exit }' "$scratch/used")"
			echo "$position omits '$flag', which $package documents as required and refuses to start without" >&2
			status=1
		fi
	done <"$scratch/required.list"
done <"$scratch/packages.unique"

if [[ "$status" -ne 0 ]]; then
	exit 1
fi
echo "container command and documented flag contract passed for $(paste -sd' ' - <"$scratch/packages.unique")"
