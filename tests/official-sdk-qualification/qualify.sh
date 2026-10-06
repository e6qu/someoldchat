#!/bin/sh

set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/sameoldchat-sdk-qualification.XXXXXX")
fixture_pid=""
coverage_log="$work/sdk-methods.log"
export SAMEOLDCHAT_SDK_COVERAGE_LOG="$coverage_log"

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
	# A fixture left running by another run would answer the readiness probe
	# below, and the suites would then qualify that process instead of this
	# build. Refuse rather than race it for the port.
	if curl -fsS "http://127.0.0.1:18080/qualification/ready" >/dev/null 2>&1; then
		echo "127.0.0.1:18080 already serves a fixture; stop it before qualifying" >&2
		exit 1
	fi
	"$work/fixture" &
	fixture_pid=$!
	ready=0
	for _ in $(seq 1 40); do
		if curl -fsS "http://127.0.0.1:18080/qualification/ready" >/dev/null 2>&1; then
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

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/web-api@8.2.0')
require_hash "$work/npm/$npm_tarball" 5d046d13a3cda62aae3cc49092542d32655424120ffcbbc0c4d601986b0b00aa
# The same suite installs an app through @slack/oauth's InstallProvider.
oauth_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/oauth@4.0.0')
require_hash "$work/npm/$oauth_tarball" 4cfc0b04698885a41a50e902cc57e0fa3cb08fa96b8a13d7d9e5f1d7c024abc4
npm install --prefix "$work/node-web" --no-save --ignore-scripts "$work/npm/$npm_tarball" "$work/npm/$oauth_tarball"
cp "$root/tests/official-sdk-qualification/node-web-api/qualification.mjs" "$work/node-web/qualification.mjs"
(cd "$work/node-web" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/socket-mode@3.1.0')
require_hash "$work/npm/$npm_tarball" 9c48f88dae7504c8ba31cbcedca4ffd6b09baa951dd19103255635ba578cb7ec
npm install --prefix "$work/node-socket-mode" --no-save --ignore-scripts "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-socket-mode/qualification.mjs" "$work/node-socket-mode/qualification.mjs"
(cd "$work/node-socket-mode" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/rtm-api@7.0.4')
require_hash "$work/npm/$npm_tarball" 59ace7f544d2f724f21239e19169976c619e89b397c3fc90cf7fb269f7f7dbe9
npm install --prefix "$work/node-rtm-api" --no-save --ignore-scripts "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-rtm-api/qualification.mjs" "$work/node-rtm-api/qualification.mjs"
(cd "$work/node-rtm-api" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

npm_tarball=$(npm pack --silent --pack-destination "$work/npm" '@slack/bolt@5.1.0')
require_hash "$work/npm/$npm_tarball" 021ba2a80736c9afe6ef5917a26b2b95eec0f6e46f2d704ccd189560c15567c0
npm install --prefix "$work/node-bolt" --no-save --ignore-scripts "$work/npm/$npm_tarball"
cp "$root/tests/official-sdk-qualification/node-bolt/qualification.mjs" "$work/node-bolt/qualification.mjs"
(cd "$work/node-bolt" && SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ node qualification.mjs)
stop_fixture
start_fixture

python3 -m pip download --disable-pip-version-check --no-deps --only-binary=:all: --dest "$work/python" slack-sdk==3.45.0
python_wheel=$(find "$work/python" -maxdepth 1 -type f -name 'slack_sdk-3.45.0-*.whl' -print -quit)
require_hash "$python_wheel" 6356d4486d1a3ad156462c5544ab1b9c076ff426a250495c08f51b7ad71eb8fb
python3 -m pip install --disable-pip-version-check --no-index --no-deps --target "$work/python-slack-sdk" "$python_wheel"
PYTHONPATH="$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ python3 "$root/tests/official-sdk-qualification/python-slack-sdk/qualification.py"
stop_fixture
start_fixture
PYTHONPATH="$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ SAMEOLDCHAT_QUALIFICATION_URL=http://127.0.0.1:18080 python3 "$root/tests/official-sdk-qualification/python-socket-mode/qualification.py"
stop_fixture
start_fixture
# slack_sdk.rtm_v2 ships in the same wheel and needs no other package: it
# speaks the WebSocket through slack_sdk's builtin connection.
PYTHONPATH="$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ python3 "$root/tests/official-sdk-qualification/python-rtm/qualification.py"
stop_fixture
start_fixture

python3 -m pip download --disable-pip-version-check --no-deps --only-binary=:all: --dest "$work/python" slack-bolt==1.30.0
python_wheel=$(find "$work/python" -maxdepth 1 -type f -name 'slack_bolt-1.30.0-*.whl' -print -quit)
require_hash "$python_wheel" 81f5bc46e79516d23d5e2a31dded6304dd1b8b6b72c0083f2f31d5d801e262c4
python3 -m pip install --disable-pip-version-check --no-index --no-deps --target "$work/python-bolt" "$python_wheel"
PYTHONPATH="$work/python-bolt:$work/python-slack-sdk" SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ python3 "$root/tests/official-sdk-qualification/python-bolt/qualification.py"
stop_fixture
start_fixture

mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" dependency:go-offline compile
java_api_jar="$HOME/.m2/repository/com/slack/api/slack-api-client/1.52.0/slack-api-client-1.52.0.jar"
java_bolt_jar="$HOME/.m2/repository/com/slack/api/bolt/1.52.0/bolt-1.52.0.jar"
java_websocket_jar="$HOME/.m2/repository/org/java-websocket/Java-WebSocket/1.6.0/Java-WebSocket-1.6.0.jar"
require_hash "$java_api_jar" a146a48e823e95932c828d6f455d5b269bd0d4ba8f9d3648474d3b8611a7764f
require_hash "$java_bolt_jar" b8f6f61f06d3bf050df9aedf2dbaed8b79123661d9d8e3632e8fed334e91c393
require_hash "$java_websocket_jar" eae29213e4f16515639c28957200f011b3967fffcada1962cf0255d24919c22f
mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" dependency:build-classpath -Dmdep.outputFile="$work/java-classpath"
# The server's per-method tiers are tested against the vendored copy of the
# pinned SDK's published rate-limit table; the copy must be that table.
java -cp "$root/tests/official-sdk-qualification/java-slack-api/target/classes:$(cat "$work/java-classpath")" sameoldchat.qualification.RateLimitTable >"$work/methods-rate-limits.json"
if ! cmp -s "$work/methods-rate-limits.json" "$root/specs/upstream/java-slack-sdk/methods-rate-limits.json"; then
	echo "specs/upstream/java-slack-sdk/methods-rate-limits.json differs from the pinned slack-api-client's MethodsRateLimits" >&2
	diff "$root/specs/upstream/java-slack-sdk/methods-rate-limits.json" "$work/methods-rate-limits.json" >&2 || true
	exit 1
fi
SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ mvn -q -f "$root/tests/official-sdk-qualification/java-slack-api/pom.xml" exec:java
SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ SAMEOLDCHAT_QUALIFICATION_URL=http://127.0.0.1:18080 java -cp "$root/tests/official-sdk-qualification/java-slack-api/target/classes:$(cat "$work/java-classpath")" sameoldchat.qualification.SocketModeQualification
SAMEOLDCHAT_API_URL=http://127.0.0.1:18080/api/ java -cp "$root/tests/official-sdk-qualification/java-slack-api/target/classes:$(cat "$work/java-classpath")" sameoldchat.qualification.BoltQualification

# The suite used to prove only that a large script exited successfully. Compare
# the exact Web API paths emitted by the pinned official clients with the
# method-level evidence claimed by the compatibility ledger. The comparison is
# fail-closed: no method may remain SDK-compatible merely because another
# method happened to pass in the same large script.
(cd "$root" && GOCACHE="$root/.cache/go-build" go run ./cmd/sdkcoverage -input "$coverage_log" -require-claimed)
