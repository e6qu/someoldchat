#!/usr/bin/env bash
# Starts the SameOldChat server with exactly the environment and secret keys a
# Terraform module exports and asserts the binary accepts them.
#
# `terraform/ecs-runtime` shipped an `environment` output that always set
# SAMEOLDCHAT_OIDC_ISSUER and a `secrets` output that always set
# SAMEOLDCHAT_SESSION_TOKEN. cmd/server refuses that pair outright — a static
# browser session shared by every holder cannot coexist with real single sign-on
# — so the module was dead on arrival: every task built from its own documented
# outputs exited 2 at startup. Every gate was green, because no gate ever ran the
# binary against a module's output. `terraform validate` type-checks a map of
# strings, and `terraform plan` needs credentials.
#
# The binary's own `-check-config` is the authority: it runs the whole startup
# configuration contract and then returns without opening a store, dialing a
# peer, or binding a listener, so this check cannot drift from what a real task
# would do.
#
# Where the module exports a *literal* value, that literal is what the binary is
# started with. Substituting a representative value for every key meant the
# module's own values were never tested: changing the exported
# `SAMEOLDCHAT_CHAT_MODE = "local"` to `"grpc"` left this check at exit 0 while
# the binary with the module's real values exits 2 with "grpc chat requires
# address, server CA/name, and client certificate/key". A module that hardcodes
# a refused literal shipped a crash-looping task with this gate green, which is
# the precise failure the gate was written for.
#
# A value the module takes from a variable or a resource attribute cannot be
# known here, so those keep a representative value. The contract under test is
# then the *key set* a module exports, and a key with neither a literal nor a
# representative value is an error rather than a silent skip: a newly exported
# key must be classified deliberately.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$root"

: "${GOCACHE:=$root/.cache/go-build}"
export GOCACHE
export GOWORK=off

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT INT TERM
status=0
covered=0

# representative_value echoes a value the binary should accept for a key, or
# nothing when the key is unknown to this check.
representative_value() {
	case "$1" in
	SAMEOLDCHAT_CHAT_MODE) echo "local" ;;
	SAMEOLDCHAT_STORE) echo "memory" ;;
	SAMEOLDCHAT_DATABASE_URL) echo "postgres://sameoldchat@postgres.example/sameoldchat" ;;
	SAMEOLDCHAT_API_TOKEN) echo "xoxb-representative-token" ;;
	SAMEOLDCHAT_SESSION_TOKEN) echo "representative-session" ;;
	SAMEOLDCHAT_AUTH_WORKSPACE) echo "Tdev" ;;
	SAMEOLDCHAT_AUTH_LOOKUP_USER) echo "Udev" ;;
	SAMEOLDCHAT_AUTH_PUBLIC_URL) echo "https://chat.example.com" ;;
	SAMEOLDCHAT_AUTH_COOKIE_DOMAIN) echo "example.com" ;;
	SAMEOLDCHAT_AUTH_STATE_KEY_HEX) echo "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" ;;
	SAMEOLDCHAT_BOOTSTRAP_ADMIN_EMAIL) echo "admin@example.com" ;;
	SAMEOLDCHAT_BLOB_S3_BUCKET) echo "sameoldchat-blobs-representative" ;;
	SAMEOLDCHAT_BLOB_S3_PREFIX) echo "blobs/" ;;
	SAMEOLDCHAT_OIDC_ISSUER) echo "https://id.example.com" ;;
	SAMEOLDCHAT_OIDC_CLIENT_ID) echo "representative-client" ;;
	SAMEOLDCHAT_OIDC_CLIENT_SECRET) echo "representative-secret" ;;
	SAMEOLDCHAT_RELEASE_REVISION) echo "0123456789ab" ;;
	SAMEOLDCHAT_METRICS_LISTEN) echo "127.0.0.1:9464" ;;
	SAMEOLDCHAT_APP_TOKEN) echo "xapp-representative" ;;
	SAMEOLDCHAT_APP_ID) echo "A0REPRESENTATIVE" ;;
	SAMEOLDCHAT_SOCKET_HOST) echo "chat.example.com:443" ;;
	SAMEOLDCHAT_SOCKET_TLS) echo "1" ;;
	*) return 1 ;;
	esac
}

modules() {
	find deploy terraform -name 'outputs.tf' -not -path '*/.terraform/*' | sort |
		while IFS= read -r file; do
			if grep -q 'SAMEOLDCHAT_' "$file"; then
				dirname "$file"
			fi
		done
}

module_list="$(modules)"
if [[ -z "$module_list" ]]; then
	echo 'no Terraform module exports SAMEOLDCHAT_ environment keys; this check would silently cover nothing' >&2
	exit 1
fi

while IFS= read -r module; do
	[[ -n "$module" ]] || continue
	# Comments are stripped first: a comment naming a key the module deliberately
	# does *not* export must not be read as an export.
	stripped="$(sed 's/#.*//' "$module/outputs.tf")"
	keys="$(printf '%s\n' "$stripped" | grep -oE 'SAMEOLDCHAT_[A-Z0-9_]+' | sort -u)"
	[[ -n "$keys" ]] || continue
	# The module's own literal values, which take precedence over anything this
	# script would substitute.
	printf '%s\n' "$stripped" |
		sed -nE 's/^[[:space:]]*(SAMEOLDCHAT_[A-Z0-9_]+)[[:space:]]*=[[:space:]]*"([^"]*)"[[:space:]]*$/\1 \2/p' >"$scratch/literals"
	: >"$scratch/environment"
	unknown=0
	while IFS= read -r key; do
		[[ -n "$key" ]] || continue
		if literal="$(awk -v key="$key" '$1 == key { $1 = ""; sub(/^ /, ""); print; exit }' "$scratch/literals")" && [[ -n "$literal" ]]; then
			printf '%s=%s\n' "$key" "$literal" >>"$scratch/environment"
			continue
		fi
		if ! value="$(representative_value "$key")"; then
			echo "$module/outputs.tf exports $key, which scripts/check-terraform-module-startup.sh has no representative value for; add one so the key is covered rather than skipped" >&2
			unknown=1
			continue
		fi
		printf '%s=%s\n' "$key" "$value" >>"$scratch/environment"
	done <<<"$keys"
	if [[ "$unknown" -ne 0 ]]; then
		status=1
		continue
	fi
	# env -i so only the module's own keys reach the binary: a SAMEOLDCHAT_
	# variable already set in the developer's shell must not make a module that
	# omits one look complete. Only the Go toolchain's own variables are passed
	# through.
	assignments=()
	while IFS= read -r assignment; do
		[[ -n "$assignment" ]] || continue
		assignments+=("$assignment")
	done <"$scratch/environment"
	base=(PATH="$PATH" HOME="$HOME" GOCACHE="$GOCACHE" GOWORK=off)
	for name in GOPATH GOMODCACHE GOTOOLCHAIN GOFLAGS GOPROXY GONOSUMCHECK GOPRIVATE TMPDIR; do
		value="$(printenv "$name" || true)"
		[[ -n "$value" ]] || continue
		base+=("$name=$value")
	done
	output="$scratch/output"
	if env -i "${base[@]}" "${assignments[@]}" go run ./cmd/server -check-config >"$output" 2>&1; then
		covered=$((covered + 1))
		echo "$module: cmd/server accepts the exported environment and secret keys"
	else
		echo "$module exports an environment cmd/server refuses, so a task configured from this module cannot start:" >&2
		sed 's/^/    /' "$output" >&2
		echo "    keys: $(paste -sd' ' - <"$scratch/environment" | sed 's/=[^ ]*//g')" >&2
		status=1
	fi
done <<<"$module_list"

if [[ "$covered" -eq 0 && "$status" -eq 0 ]]; then
	echo 'no module startup contract was exercised; this check would silently cover nothing' >&2
	exit 1
fi
if [[ "$status" -ne 0 ]]; then
	exit 1
fi
echo 'terraform module startup contract passed'
