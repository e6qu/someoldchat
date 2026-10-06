# SDK qualification

Qualification is fail-closed. A suite is recorded as passed only after the
exact pinned artifact has been installed and its executable suite has passed
against the seeded local fixture.

The Node Web API suite uses `@slack/web-api` 8.2.0 and `@slack/oauth` 4.0.0, the Node Bolt suite uses
`@slack/bolt` 5.1.0, the Node Socket Mode suite uses `@slack/socket-mode`
3.1.0, and the Node Real Time Messaging suite uses `@slack/rtm-api` 7.0.4.
The Python Web API, Socket Mode, and Real Time Messaging (`slack_sdk.rtm_v2`)
suites use `slack-sdk` 3.45.0, the
Python Bolt suite uses `slack-bolt` 1.30.0, the Java Web API and Socket Mode
suites use `com.slack.api:slack-api-client` 1.52.0, the Java Bolt suite uses
`com.slack.api:bolt` 1.52.0, and the Deno suite uses `deno-slack-runtime` 1.1.3. Their immutable artifact
hashes and suite paths are recorded in [`../../specs/sdk-compatibility.yaml`](../../specs/sdk-compatibility.yaml).

The fixture registers the Slack handler exactly as the production server does
with its default `-api-rate-limit`, limiter included, so a route the limited
registration fails to serve fails qualification. The suites stay inside the
limiter's budgets: each method's published tier (Tier 1 admits a burst of
three, then one call a minute; Tier 2 twenty a minute) and five posts in a
burst per channel. The Java suite's `RateLimitTable` prints the pinned
`slack-api-client`'s `MethodsRateLimits` table, and `qualify.sh` fails when
`specs/upstream/java-slack-sdk/methods-rate-limits.json`, which the server's
tiers are tested against, differs from it.

The Node, Python, and Java Web API suites exercise presence-sensitive rich-message
updates through the SDKs' own array encoders: omitted blocks and attachments
must survive, while explicit empty arrays must remove them. They also parse the
documented user-token `search.messages`, `search.files`, and legacy combined
`search.all` envelopes. Message results cover channel, user, permalink, total,
legacy paging, and pagination fields, including Slack's `cursor="*"` first-page
convention and 100-result clamp; file results are matched against a real hosted
file through each SDK's generated response types.

The Node and Python Web API suites upload through the SDKs' current v2 helpers
and download `url_private` (and, in Node, `permalink_public`) through the
absolute URLs the file objects carry. They also install an app from nothing:
Node through `@slack/oauth`'s `InstallProvider`, Python through
`slack_sdk.oauth`'s state store, URL generator, and installation store. Each
walk reinstalls with an implied `redirect_uri` and keeps the bot user, and
completes a user-scope-only install through `oauth.v2.access`. The fixture's
`GET /qualification/authorize` stands in for the browser consent page, which
the browser suite qualifies, by approving through the same service call.

Scheduling qualification deliberately covers only Slack's public Web API:
`chat.scheduleMessage`, `chat.scheduledMessages.list`, and
`chat.deleteScheduledMessage`. Slack does not publish Web API methods for
editing or immediately sending a scheduled message. SameOldChat implements
those Slack client journeys only through its authenticated first-party service
and gRPC seam; it does not advertise invented Slack Web API methods.

The Node and Python Bolt suites run a remote custom function over signed
HTTP. The fixture's `POST /qualification/bolt-function` runs a workflow whose
step is the app's `approval` function and delivers `function_executed` to
Bolt; the function listener posts a button with the execution-scoped
(`xwfp-`) client Bolt builds from `bot_access_token`, `POST
/qualification/bolt-function-click` clicks it as a member, and the action
listener, which Bolt recognizes as function-scoped from `function_data`,
calls `complete()`. `GET /qualification/bolt-function-state` shows the
message belonging to the execution and the execution completed with the
listener's outputs.

The official Node Socket Mode client also consumes a manifest-derived slash
command envelope whose `should_escape` option resolves a user mention, public
channel, and URL. This proves the escaped payload survives the real SDK's
envelope parser and acknowledgement path rather than only a hand-authored Go
fixture.

To run the reproducible Node, Python, and Java suites with artifact hash
verification, run:

```sh
make sdk-qualification
```

The runner starts the test fixture, downloads the exact pinned artifacts into
a temporary directory, verifies each recorded SHA-256 digest, and runs the
checked-in suites. The fixture records the exact `/api/{method}` paths emitted
by those clients and `cmd/sdkcoverage` compares the observed set with every
method claimed at `sdk-compatible` or above. The comparison is fail-closed:
every such method must be observed at the shared fixture or, for
`functions.complete*`, at the Deno runtime suite's verified receiver. The
runner does not modify the repository. It requires Go, Node.js, npm, Python,
pip, curl, Java, Maven, Deno, and tar.

The individual suite commands remain useful for debugging:

```sh
go run ./tests/official-sdk-qualification/node-web-api/fixture
npm install --prefix /tmp/soc-sdk-web-run @slack/web-api@8.2.0 @slack/oauth@4.0.0
cp tests/official-sdk-qualification/node-web-api/qualification.mjs /tmp/soc-sdk-web-run/qualification.mjs
node /tmp/soc-sdk-web-run/qualification.mjs

python3 -m pip install --target /tmp/soc-sdk-python slack-sdk==3.45.0
PYTHONPATH=/tmp/soc-sdk-python python3 tests/official-sdk-qualification/python-slack-sdk/qualification.py

python3 -m pip install --target /tmp/soc-sdk-python-bolt slack-bolt==1.30.0
PYTHONPATH=/tmp/soc-sdk-python-bolt python3 tests/official-sdk-qualification/python-bolt/qualification.py

deno run --allow-env --allow-net --allow-read --allow-write --allow-run=deno tests/official-sdk-qualification/deno-slack-runtime/qualification.ts

mvn -q -f tests/official-sdk-qualification/java-slack-api/pom.xml compile exec:java
mvn -q -f tests/official-sdk-qualification/java-slack-api/pom.xml dependency:build-classpath -Dmdep.outputFile=/tmp/soc-java-classpath
java -cp "tests/official-sdk-qualification/java-slack-api/target/classes:$(cat /tmp/soc-java-classpath)" sameoldchat.qualification.BoltQualification
```

The Deno Slack runtime suite validates the `functions.completeSuccess` protocol
adapter. The fixture is test-only and is not a production composition root.

Related documents: [SDK source inventory](../../specs/sdk-compatibility.yaml),
[compatibility specification](../../specs/api-compatibility.md), and
[repository build instructions](../../README.md).
