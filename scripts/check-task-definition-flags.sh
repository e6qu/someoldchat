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
# The Go side is the binary's own `flag` usage output, which is authoritative for
# every binding style (`flag.String`, `flag.XxxVar`, a `FlagSet` bound by another
# type) and cannot drift from the parsed flag set. A flag whose usage text ends in
# "(required)" must appear in the command; any flag in the command must be
# defined.
#
# A command list holding literal flag tokens with no `flag-contract` annotation is
# an error, so a new task definition cannot opt out of the check by omission.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

: "${GOCACHE:=$root/.cache/go-build}"
export GOCACHE
export GOWORK=off

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT INT TERM
status=0

# Emits one "<file>:<line> <package> <flag>" record per literal flag token found
# in an annotated container command, and one "<file>:<line> - <flag>" record when
# the annotation is missing. The annotation is scoped to the resource block it
# appears in so it cannot leak into an unrelated task definition.
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
			if (!collecting && $0 ~ /^[[:space:]]*command[[:space:]]*=/) {
				collecting = 1
				depth = 0
				start = FNR
				found = 0
				owner = contract
			}
			if (!collecting) next
			rest = $0
			while (match(rest, /"-[a-z][a-z0-9-]*"/)) {
				token = substr(rest, RSTART + 1, RLENGTH - 2)
				found = found + 1
				printf "%s:%d %s %s\n", FILENAME, start, (owner == "" ? "-" : owner), token
				rest = substr(rest, RSTART + RLENGTH)
			}
			for (i = 1; i <= length($0); i++) {
				character = substr($0, i, 1)
				if (character == "[") depth++
				else if (character == "]") depth--
			}
			if (depth <= 0) collecting = 0
		}
	' "$1"
}

terraform_files() {
	find deploy terraform -name '*.tf' -not -path '*/.terraform/*' | sort
}

while IFS= read -r file; do
	extract_command_flags "$file" >>"$scratch/used"
done <<<"$(terraform_files)"
touch "$scratch/used"

while read -r position package flag; do
	[[ -n "${position:-}" ]] || continue
	if [[ "$package" == "-" ]]; then
		echo "$position passes flag '$flag' from a command with no '# flag-contract: cmd/<binary>' annotation, so it cannot be verified against any binary" >&2
		status=1
		continue
	fi
	printf '%s\n' "$package" >>"$scratch/packages"
done <"$scratch/used"

if [[ ! -s "$scratch/packages" ]]; then
	echo 'no annotated Terraform container command was found; this check would silently cover nothing' >&2
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
	if ! grep -Eq '^[[:space:]]+-[a-z]' "$usage"; then
		echo "could not enumerate the flags of $package; 'go run ./$package -h' produced no flag usage:" >&2
		sed 's/^/    /' "$usage" >&2
		status=1
		continue
	fi
	awk '
		match($0, /^[[:space:]]+-[a-z][a-z0-9-]*/) {
			name = substr($0, RSTART, RLENGTH)
			sub(/^[[:space:]]+/, "", name)
			current = name
			print "defined " current
			next
		}
		# Only an unconditional "(required)" at the end of the usage text counts.
		# "(required for -snapshot-store=filesystem)" is a conditional requirement
		# this check cannot evaluate, so it must not be demanded of every caller.
		current != "" && /\(required\)[[:space:]]*$/ { print "required " current; current = "" }
	' "$usage" | sed "s|^|$package |" >>"$scratch/declared"
done <"$scratch/packages.unique"
touch "$scratch/declared"

while IFS= read -r package; do
	[[ -n "$package" ]] || continue
	awk -v package="$package" '$2 == "defined" && $1 == package { print $3 }' "$scratch/declared" | sort -u >"$scratch/defined.list"
	awk -v package="$package" '$2 == "required" && $1 == package { print $3 }' "$scratch/declared" | sort -u >"$scratch/required.list"
	awk -v package="$package" '$2 == package { print $3 }' "$scratch/used" | sort -u >"$scratch/used.list"
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
echo "terraform container command flag contract passed for $(paste -sd' ' - <"$scratch/packages.unique")"
