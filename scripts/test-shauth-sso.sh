#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
set -eu

unset CDPATH
root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
shauth_root=${SHAUTH_SOURCE_DIR:?SHAUTH_SOURCE_DIR must point to the exact Shauth checkout}
expected_shauth_commit=0fda680cba964e5768ed75a9c3e5b7230c418ca6

for command in awk curl docker git go jq node openssl; do
	command -v "$command" >/dev/null 2>&1 || {
		printf '%s is required\n' "$command" >&2
		exit 1
	}
done
docker compose version >/dev/null

actual_shauth_commit=$(git -C "$shauth_root" rev-parse HEAD)
if [ "$actual_shauth_commit" != "$expected_shauth_commit" ]; then
	printf 'Shauth checkout is %s; expected %s\n' "$actual_shauth_commit" "$expected_shauth_commit" >&2
	exit 1
fi
test -f "$shauth_root/compose.yaml"
test -f "$shauth_root/validator/validate.mjs"
test -d "$root/tests/browser/node_modules/playwright"

work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT
trap 'exit 130' INT TERM
provider_project=someoldchat-shauth-provider
primary_pid=
witness_pid=

GOWORK=off GOCACHE="$root/.cache/go-build" go build -trimpath -o "$work_dir/sameoldchat" ./cmd/server
release_revision="sha256:$(openssl dgst -sha256 -r "$work_dir/sameoldchat" | awk '{print $1}')"

ports=$(node - <<'NODE'
const net = require("node:net");
const servers = Array.from({ length: 6 }, () => net.createServer());
Promise.all(servers.map((server) => new Promise((resolve, reject) => {
  server.once("error", reject);
  server.listen(0, "127.0.0.1", () => resolve(server.address().port));
}))).then(async (values) => {
  process.stdout.write(`${values.join(" ")}\n`);
  await Promise.all(servers.map((server) => new Promise((resolve) => server.close(resolve))));
}).catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
NODE
)
IFS=' ' read -r postgres_port hydra_public_port hydra_admin_port shauth_port primary_port witness_port <<EOF
$ports
EOF

provider_origin="http://localhost:${shauth_port}"
hydra_public_origin="http://127.0.0.1:${hydra_public_port}"
primary_origin="http://someoldchat-primary.localhost:${primary_port}"
witness_origin="http://someoldchat-witness.localhost:${witness_port}"
NO_PROXY="localhost,127.0.0.1,someoldchat-primary.localhost,someoldchat-witness.localhost${NO_PROXY:+,$NO_PROXY}"
no_proxy=$NO_PROXY
export NO_PROXY no_proxy

postgres_password=$(openssl rand -hex 32)
hydra_secret=$(openssl rand -base64 48 | tr -d '\n')
admin_password=$(openssl rand -base64 48 | tr -d '\n')
validator_token=$(openssl rand -hex 48)
validation_status_token=$(openssl rand -hex 48)
primary_client_secret=$(openssl rand -hex 32)
witness_client_secret=$(openssl rand -hex 32)
primary_api_token=$(openssl rand -hex 32)
witness_api_token=$(openssl rand -hex 32)
primary_session_token=$(openssl rand -hex 32)
witness_session_token=$(openssl rand -hex 32)
primary_state_key=$(openssl rand -hex 48)
witness_state_key=$(openssl rand -hex 48)

bootstrap_apps=$(jq -cn \
	--arg primary_origin "$primary_origin" \
	--arg witness_origin "$witness_origin" \
	--arg primary_secret "$primary_client_secret" \
	--arg witness_secret "$witness_client_secret" \
	--arg release "$release_revision" '
[
  {
    slug:"someoldchat-primary", name:"SameOldChat primary", description:"SameOldChat primary SSO acceptance application.",
    launch_url:($primary_origin + "/"), oidc_client_id:"someoldchat-primary", oidc_client_secret:$primary_secret,
    redirect_uris:[($primary_origin + "/auth/oidc/callback")],
    post_logout_redirect_uris:[($primary_origin + "/auth/shauth/logout/complete")],
    backchannel_logout_uri:($primary_origin + "/auth/oidc/backchannel-logout"),
    health_url:($primary_origin + "/healthz"), monitoring_url:"",
    validation_url:($primary_origin + "/auth/validation"), signed_out_url:($primary_origin + "/signed-out"),
    release_revision:$release
  },
  {
    slug:"someoldchat-witness", name:"SameOldChat witness", description:"SameOldChat global logout witness.",
    launch_url:($witness_origin + "/"), oidc_client_id:"someoldchat-witness", oidc_client_secret:$witness_secret,
    redirect_uris:[($witness_origin + "/auth/oidc/callback")],
    post_logout_redirect_uris:[($witness_origin + "/auth/shauth/logout/complete")],
    backchannel_logout_uri:($witness_origin + "/auth/oidc/backchannel-logout"),
    health_url:($witness_origin + "/healthz"), monitoring_url:"",
    validation_url:($witness_origin + "/auth/validation"), signed_out_url:($witness_origin + "/signed-out"),
    release_revision:$release
  }
]')

cat >"$work_dir/provider-ports.yaml" <<EOF
services:
  postgres:
    ports: !override
      - "127.0.0.1:${postgres_port}:5432"
  hydra:
    ports: !override
      - "127.0.0.1:${hydra_public_port}:4444"
      - "127.0.0.1:${hydra_admin_port}:4445"
    extra_hosts:
      - "someoldchat-primary.localhost:host-gateway"
      - "someoldchat-witness.localhost:host-gateway"
  shauth:
    ports: !override
      - "127.0.0.1:${shauth_port}:8080"
    extra_hosts:
      - "someoldchat-primary.localhost:host-gateway"
      - "someoldchat-witness.localhost:host-gateway"
EOF

provider_compose() {
	env \
		POSTGRES_PASSWORD="$postgres_password" \
		HYDRA_SYSTEM_SECRET="$hydra_secret" \
		HYDRA_DSN="postgres://shauth:${postgres_password}@postgres:5432/hydra?sslmode=disable" \
		HYDRA_PUBLIC_URL="$provider_origin" \
		SHAUTH_PUBLIC_URL="$provider_origin" \
		SHAUTH_DATABASE_URL="postgres://shauth:${postgres_password}@postgres:5432/shauth?sslmode=disable" \
		GITHUB_CLIENT_ID=someoldchat-integration \
		GITHUB_CLIENT_SECRET=someoldchat-integration-secret \
		SHAUTH_BOOTSTRAP_ADMIN_PASSWORD="$admin_password" \
		SHAUTH_VALIDATOR_TOKEN="$validator_token" \
		SHAUTH_VALIDATION_STATUS_TOKEN="$validation_status_token" \
		SHAUTH_BOOTSTRAP_APPS_JSON="$bootstrap_apps" \
		docker compose --project-name "$provider_project" --project-directory "$shauth_root" \
		-f "$shauth_root/compose.yaml" -f "$work_dir/provider-ports.yaml" "$@"
}

cleanup() {
	status=$?
	trap - EXIT INT TERM
	for pid in "$primary_pid" "$witness_pid"; do
		if test -n "$pid"; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	if test "$status" -ne 0; then
		for log_file in "$work_dir/primary.log" "$work_dir/witness.log"; do
			test -f "$log_file" && tail -n 120 "$log_file" >&2
		done
		provider_compose logs --no-color --tail=120 shauth hydra postgres >&2 || true
	fi
	provider_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$work_dir"
	exit "$status"
}
trap cleanup EXIT INT TERM

wait_for_url() {
	url=$1
	name=$2
	attempt=0
	while test "$attempt" -lt 180; do
		if curl --fail --silent "$url" >/dev/null 2>&1; then
			return 0
		fi
		attempt=$((attempt + 1))
		sleep 1
	done
	printf '%s did not become ready at %s\n' "$name" "$url" >&2
	return 1
}

provider_compose down --volumes --remove-orphans >/dev/null 2>&1 || true
provider_compose up --build --detach
wait_for_url "$provider_origin/healthz" Shauth
wait_for_url "$hydra_public_origin/health/ready" "Ory Hydra"

provider_compose exec -T postgres createdb -U shauth sameoldchat_primary
provider_compose exec -T postgres createdb -U shauth sameoldchat_witness

"$work_dir/sameoldchat" \
	-addr ":${primary_port}" -chat-mode local -store postgresql \
	-db "postgres://shauth:${postgres_password}@127.0.0.1:${postgres_port}/sameoldchat_primary?sslmode=disable" \
	-api-token "$primary_api_token" -session-token "$primary_session_token" \
	-bootstrap-admin-email primary-bootstrap@localhost.test \
	-auth-workspace Tdev -auth-lookup-user Udev -auth-public-url "$primary_origin" -auth-state-key-hex "$primary_state_key" \
	-oidc-issuer "$provider_origin" -oidc-client-id someoldchat-primary -oidc-client-secret "$primary_client_secret" \
	-release-revision "$release_revision" >"$work_dir/primary.log" 2>&1 &
primary_pid=$!

"$work_dir/sameoldchat" \
	-addr ":${witness_port}" -chat-mode local -store postgresql \
	-db "postgres://shauth:${postgres_password}@127.0.0.1:${postgres_port}/sameoldchat_witness?sslmode=disable" \
	-api-token "$witness_api_token" -session-token "$witness_session_token" \
	-bootstrap-admin-email witness-bootstrap@localhost.test \
	-auth-workspace Tdev -auth-lookup-user Udev -auth-public-url "$witness_origin" -auth-state-key-hex "$witness_state_key" \
	-oidc-issuer "$provider_origin" -oidc-client-id someoldchat-witness -oidc-client-secret "$witness_client_secret" \
	-release-revision "$release_revision" >"$work_dir/witness.log" 2>&1 &
witness_pid=$!

wait_for_url "http://127.0.0.1:${primary_port}/healthz" "SameOldChat primary"
wait_for_url "http://127.0.0.1:${witness_port}/healthz" "SameOldChat witness"

for process_id in "$primary_pid" "$witness_pid"; do
	if ps eww -p "$process_id" | grep -F "$validator_token" >/dev/null 2>&1; then
		echo "Shauth validator token leaked into a SameOldChat process" >&2
		exit 1
	fi
done

for origin in "$primary_origin" "$witness_origin"; do
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' --header "Authorization: Bearer ${validator_token}" "$origin/api/auth.test")
	test "$status" = 401
	status=$(curl --silent --output /dev/null --write-out '%{http_code}' --header "Authorization: Basic $(printf 'shauth-validator:%s' "$validator_token" | openssl base64 -A)" "$origin/app")
	test "$status" = 303
done

mkdir "$work_dir/validator"
cp "$shauth_root/validator/validate.mjs" "$shauth_root/validator/security.mjs" "$shauth_root/validator/readiness.mjs" "$work_dir/validator/"
ln -s "$root/tests/browser/node_modules" "$work_dir/node_modules"

create_bootstraps() {
	next=$1
	curl --fail --silent --show-error \
		--request POST \
		--header "Authorization: Bearer ${validator_token}" \
		--header 'Content-Type: application/json' \
		--data "{\"next\":[\"${next}\",\"/\"]}" \
		"$provider_origin/internal/validator/browser-bootstraps"
}

run_direction() {
	direction=$1
	if test "$direction" = from_shauth; then
		bootstraps=$(create_bootstraps /apps)
	else
		bootstraps=$(create_bootstraps /)
	fi
	job=$(jq -cn \
		--arg direction "$direction" \
		--arg release "$release_revision" \
		--arg provider "$provider_origin" \
		--arg primary "$primary_origin" \
		--arg witness "$witness_origin" \
		--argjson bootstraps "$(printf '%s' "$bootstraps" | jq '.urls')" '
  {
    id:("someoldchat-" + $direction), managed_app_id:"00000000-0000-4000-8000-000000000101",
    app_slug:"someoldchat-primary", app_name:"SameOldChat primary", oidc_client_id:"someoldchat-primary",
    launch_url:($primary + "/"), validation_url:($primary + "/auth/validation"), signed_out_url:($primary + "/signed-out"),
    logout_bridge_url:($primary + "/auth/shauth/logout/complete"), direction:$direction, release_revision:$release,
    shauth_url:$provider, bootstrap_urls:$bootstraps,
    witness:{managed_app_id:"00000000-0000-4000-8000-000000000102", app_slug:"someoldchat-witness", app_name:"SameOldChat witness", oidc_client_id:"someoldchat-witness", launch_url:($witness + "/"), validation_url:($witness + "/auth/validation"), signed_out_url:($witness + "/signed-out"), logout_bridge_url:($witness + "/auth/shauth/logout/complete"), release_revision:$release}
  }')
	result=$(printf '%s' "$job" | \
		SHAUTH_VALIDATION_USERNAME=shauth-validator \
		SHAUTH_VALIDATION_EMAIL=shauth-validator@localhost.test \
		node "$work_dir/validator/validate.mjs")
	printf '%s\n' "$result" | jq --exit-status '.status == "passed" and .failure == ""' >/dev/null || {
		printf 'Shauth %s validation failed: %s\n' "$direction" "$(printf '%s' "$result" | jq -r '.failure')" >&2
		return 1
	}
}

run_direction from_app
run_direction from_shauth

printf 'SameOldChat passed direct, catalog, silent SSO, app logout, provider logout, identity, release, fail-closed, and credential-boundary validation against Shauth %s.\n' "$expected_shauth_commit"
