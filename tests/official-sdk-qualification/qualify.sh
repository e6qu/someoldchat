#!/bin/sh

set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/sameoldchat-sdk-qualification.XXXXXX")
fixture_pid=""

cleanup() {
	status=$?
	stop_fixture
	rm -rf "$work"
	exit "$status"
}
trap cleanup EXIT HUP INT TERM

hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

require_hash() {
	actual=$(hash_file "$1")
	if [ "$actual" != "$2" ]; then
		echo "sha256 mismatch for $1: got $actual, want $2" >&2
		exit 1
	fi
}

stop_fixture() {
	if [ -n "$fixture_pid" ]; then
		kill "$fixture_pid" 2>/dev/null || true
		wait "$fixture_pid" 2>/dev/null || true
		fixture_pid=""
	fi
}

start_fixture() {
	stop_fixture
	"$work/fixture" &
	fixture_pid=$!
	ready=0
	for _ in $(seq 1 40); do
		if curl -fsS "http://127.0.0.1:18080/api/api.test?token=xoxb-test" >/dev/null 2>&1; then
			ready=1
			break
		fi
		if ! kill -0 "$fixture_pid" 2>/dev/null; then
			echo "SDK fixture exited before becoming ready" >&2
			exit 1
		fi
		sleep 1
	done
	if [ "$ready" -ne 1 ]; then
		echo "SDK fixture did not become ready" >&2
		exit 1
	fi
}

mkdir -p "$work/npm" "$work/python" "$work/deno"

deno_archive="$work/deno-slack-runtime-1.1.3.tar.gz"
curl -fsSL 'https://github.com/slackapi/deno-slack-runtime/archive/refs/tags/1.1.3.tar.gz' -o "$deno_archive"
require_hash "$deno_archive" bf39d64147ce9f8fe16fa819ad4a89e791b73bd1fbcca09987455faec3b5423b
tar -xzf "$deno_archive" -C "$work/deno"
DENO_SLACK_RUNTIME_URL="file://$work/deno/deno-slack-runtime-1.1.3/src/mod.ts" env -u LD_LIBRARY_PATH deno run --quiet \
	--allow-env --allow-net --allow-read --allow-write --allow-run=deno \
	"$root/tests/official-sdk-qualification/deno-slack-runtime/qualification.ts"

(cd "$root" && go build -o "$work/fixture" ./tests/official-sdk-qualification/node-web-api/fixture)

start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/web-api@7.19.0')
require_hash "$work/npm/$npm_tarball" afaeeab8f5de2c0b59c0306088a0b3db02531e1c9e2c149250c0038473f9c99a
npm install --prefix "$work/node-web" --no-save "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-web-api/qualification.mjs" "$work/node-web/qualification.mjs"
(cd "$work/node-web" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/socket-mode@3.0.0')
require_hash "$work/npm/$npm_tarball" 3d70683ca2872323150747e9611f4de35d9df333bdc6321bb4360c9f1d165fe6
npm install --prefix "$work/node-socket-mode" --no-save "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-socket-mode/qualification.mjs" "$work/node-socket-mode/qualification.mjs"
(cd "$work/node-socket-mode" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/bolt@4.7.3')
require_hash "$work/npm/$npm_tarball" 455afc51e720c29a70cece533ca7008e35dd122bf81dc8603f872d02a492f0de
npm install --prefix "$work/node-bolt" --no-save "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-bolt/qualification.mjs" "$work/node-bolt/qualification.mjs"
(cd "$work/node-bolt" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

python3 -m pip download --disable-pip-version-check --no-deps --only-binary=:all: --dest "$work/python" slack-sdk==3.43.0
python_wheel=$(find "$work/python" -maxdepth 1 -type f -name 'slack_sdk-3.43.0-*.whl' -print -quit)
require_hash "$python_wheel" 4b6557c65577fc172f685af218b811f9f3b4909e24cddd839ada09565f10c585
python3 -m pip install --disable-pip-version-check --no-index --no-deps --target "$work/python-slack-sdk" "$python_wheel"
PYTHONPATH="$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ python3 "$root/tests/official-sdk-qualification/python-slack-sdk/qualification.py"
stop_fixture
start_fixture
PYTHONPATH="$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ SAMEOLDCHAT_QUALIFICATION_URL=http://127.0.0.1:18080 python3 "$root/tests/official-sdk-qualification/python-socket-mode/qualification.py"
stop_fixture
start_fixture

python3 -m pip download --disable-pip-version-check --no-deps --only-binary=:all: --dest "$work/python" slack-bolt==1.28.0
python_wheel=$(find "$work/python" -maxdepth 1 -type f -name 'slack_bolt-1.28.0-*.whl' -print -quit)
require_hash "$python_wheel" 738d1ca5e7c7039b6e18103d29267ced6e18c2517053eff18991fdd593acce5c
python3 -m pip install --disable-pip-version-check --no-index --no-deps --target "$work/python-bolt" "$python_wheel"
PYTHONPATH="$work/python-bolt:$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ python3 "$root/tests/official-sdk-qualification/python-bolt/qualification.py"
stop_fixture
start_fixture

mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" dependency:go-offline compile
java_api_jar="$HOME/.m2/repository/com/slack/api/slack-api-client/1.49.0/slack-api-client-1.49.0.jar"
java_bolt_jar="$HOME/.m2/repository/com/slack/api/bolt/1.49.0/bolt-1.49.0.jar"
require_hash "$java_api_jar" eb671acc28b9618486f46f256b87235e8d358c6536cf56e6503abaec3881701f
require_hash "$java_bolt_jar" 9c298264096ba9343e55260361fcc54035a673ecc03ce5dfcee32899a6e9eca0
mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" dependency:build-classpath -Dmdep.outputFile="$work/java-classpath"
SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" exec:java
SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ java -cp "$root/tests/official-sdk-qualification/java-slack-api/target/classes:$(cat "$work/java-classpath")" sameoldchat.qualification.BoltQualification
