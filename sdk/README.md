# SDK qualification

Qualification is fail-closed. A suite is recorded as passed only after the
exact pinned artifact has been installed and its executable suite has passed
against the seeded local fixture.

The Node Web API suite uses `@slack/web-api` 7.19.0, the Node Bolt suite uses
`@slack/bolt` 4.7.3, the Python suite uses `slack-sdk` 3.43.0, and the Python
Bolt suite uses `slack-bolt` 1.28.0. Their immutable artifact hashes and suite
paths are recorded in [`../specs/sdk-compatibility.yaml`](../specs/sdk-compatibility.yaml).

To run either suite, start the fixture:

```text
go run ./sdk/node-web-api/fixture
```

Then install the exact pinned artifact into an isolated temporary environment
and run its checked-in suite:

```text
npm install --prefix /tmp/soc-sdk-web-run @slack/web-api@7.19.0
cp sdk/node-web-api/qualification.mjs /tmp/soc-sdk-web-run/qualification.mjs
node /tmp/soc-sdk-web-run/qualification.mjs

python3 -m pip install --target /tmp/soc-sdk-python slack-sdk==3.43.0
PYTHONPATH=/tmp/soc-sdk-python python3 sdk/python-slack-sdk/qualification.py

python3 -m pip install --target /tmp/soc-sdk-python-bolt slack-bolt==1.28.0
PYTHONPATH=/tmp/soc-sdk-python-bolt python3 sdk/python-bolt/qualification.py

deno run --allow-env --allow-net --allow-read --allow-write sdk/deno-slack-runtime/qualification.ts

mvn -q -f sdk/java-slack-api/pom.xml compile exec:java
# Java Bolt is run from the compiled classpath:
mvn -q -f sdk/java-slack-api/pom.xml dependency:build-classpath -Dmdep.outputFile=/tmp/soc-java-classpath
java -cp "sdk/java-slack-api/target/classes:$(cat /tmp/soc-java-classpath)" sameoldchat.qualification.BoltQualification
```

The fixture is test-only and is not a production composition root.

Related documents: [SDK source inventory](../specs/sdk-compatibility.yaml),
[compatibility specification](../specs/api-compatibility.md), and
[repository build instructions](../README.md).
