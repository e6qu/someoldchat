# SDK qualification

Qualification is fail-closed. A suite is recorded as passed only after the
exact pinned artifact has been installed and its executable suite has passed
against the seeded local fixture.

The Node Web API suite uses `@slack/web-api` 7.19.0, the Node Bolt suite uses
`@slack/bolt` 4.7.3, the Node Socket Mode suite uses `@slack/socket-mode`
3.0.0, the Python Web API and Socket Mode suites use `slack-sdk` 3.43.0, the
Python Bolt suite uses `slack-bolt` 1.28.0, and the Java Web API and Socket Mode
suites use `com.slack.api:slack-api-client` 1.49.0. Their immutable artifact
hashes and suite paths are recorded in [`../../specs/sdk-compatibility.yaml`](../../specs/sdk-compatibility.yaml).

To run the reproducible Node, Python, and Java suites with artifact hash
verification, run:

```sh
make sdk-qualification
```

The runner starts the test fixture, downloads the exact pinned artifacts into
a temporary directory, verifies each recorded SHA-256 digest, and runs the
checked-in suites. It does not modify the repository. The runner requires Go,
Node.js, npm, Python, pip, curl, Java, Maven, Deno, and tar.

The individual suite commands remain useful for debugging:

```sh
go run ./tests/official-sdk-qualification/node-web-api/fixture
npm install --prefix /tmp/soc-sdk-web-run @slack/web-api@7.19.0
cp tests/official-sdk-qualification/node-web-api/qualification.mjs /tmp/soc-sdk-web-run/qualification.mjs
node /tmp/soc-sdk-web-run/qualification.mjs

python3 -m pip install --target /tmp/soc-sdk-python slack-sdk==3.43.0
PYTHONPATH=/tmp/soc-sdk-python python3 tests/official-sdk-qualification/python-slack-sdk/qualification.py

python3 -m pip install --target /tmp/soc-sdk-python-bolt slack-bolt==1.28.0
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
