# SameOldChat Slack API and SDK compatibility specification

## What compatibility means

A Slack client, official SDK, or Slack app MUST be able to use SameOldChat by
changing only the endpoints it is configured with: the Web API base URL, the
OAuth and OpenID endpoints, the Events API and interactivity request URLs it
already declares, and the Socket Mode and RTM URLs the API hands out. Given
that change, SameOldChat MUST answer with Slack's documented API and behavior:
the same methods, arguments, response and error shapes, status codes,
headers, event envelopes, signatures, and ordering.

SameOldChat MUST NOT accommodate a client that hardcodes Slack and cannot be
pointed elsewhere. It does not impersonate Slack's hosts, issuers, or
certificates, and it does not emit `slack.com` URLs or identities in place of
its own. Where Slack's format embeds a literal Slack string that is part of
the format rather than a location, such as the `https://slack.com/team_id`
OpenID claim name, the literal is kept exactly as Slack sends it.

## Compatibility sources

The current official SDK inventory is maintained in
[`sdk-compatibility.yaml`](sdk-compatibility.yaml). It records immutable
artifacts, provenance, and executable suite results.
`make sdk-inventory-check` (`cmd/sdkcheck -require-qualified`) validates its
structure and requires immutable artifacts and passing suite records.

The repository MUST pin and retain exact revisions of:

- Slack's published Web API OpenAPI specification;
- Slack's published Events API AsyncAPI/JSON schemas;
- official Slack Node, Python, Java, and Deno SDK releases; and
- applicable official Bolt JavaScript, Python, and Java releases.

## Implementation tracking and ratchet

[`compatibility.yaml`](compatibility.yaml) contains one entry for every Web API
operation in the pinned OpenAPI document. Its status is an ordered record of
evidence:

1. `unimplemented`: no compatible handler exists;
2. `schema-compatible`: the request and response shape has a registered local
   handler;
3. `sdk-compatible`: the applicable pinned official SDK suites pass;
4. `behavior-compatible`: local behavior matches the selected contract tests;
5. `verified-against-slack`: controlled comparison with Slack has passed.

The repository must not remove an operation or silently lower its status.
CI runs `make contract-ratchet` against the base revision (the pull request
base branch, or the pushed commit's parent). A downgrade is accepted only when
the operation carries an `audit` record naming the exact prior status, a
concrete reason, and reviewable evidence, so an audit can correct an overstated
claim without making it permanent. A base revision with no ledger is treated as
an explicit bootstrap and validated with `make contract-check` instead. New
operations may enter the ledger at `unimplemented`.

### Methods outside the pinned OpenAPI snapshot

A current official method absent from the pinned OpenAPI snapshot may enter
the ledger with `provenance: slack-reference` only when its official method
reference and an executable qualification are recorded. This covers, among
others, the `canvases.*`, `bookmarks.*`, `slackLists.*`, `entity.*` (Work
Objects), and `openid.connect.*` families. Their request, response, scope, and
error behavior MUST stay aligned with the individual official method
references, against which they are qualified before their provenance is
promoted; tracking them does not claim the vendored snapshot contains them.

`openid.connect.token` keeps authorization codes single-use and rotates refresh
tokens in the selected durable store. Sign in with Slack is served at this
deployment's own URLs: `/.well-known/openid-configuration` names the
deployment's `-auth-public-url` as the issuer, `/openid/connect/authorize`
takes the relying party's `nonce`, and ID tokens are signed RS256 with a key
published at `/openid/connect/keys`. The deployment holds one signing key,
created on first use and stored sealed under the app credential key, so every
replica signs with it.

### Reporting

`make compatibility-report` prints the operation count and the number at or
above each evidence level, using `<status>-or-better` for those cumulative
counts. It separates the current Slack reference from retained legacy methods
so compatibility aliases cannot inflate the current denominator, and reports
how many methods name method-level executable evidence, how many
`sdk-compatible`-or-better claims lack it, and how many carry known deviations.
The [project status](../PLAN.md#status) records its current figures. The
target is `verified-against-slack` for every operation; a schema-compatible handler is
not behavior verification.

`make sdk-qualification` records the exact Web API paths emitted by the pinned
official clients and runs `cmd/sdkcoverage -require-claimed`. Every method at
`sdk-compatible` or above must be observed in that run; success elsewhere in
the same SDK script cannot supply method evidence. Deno function-completion
methods are recorded only after the runtime's requests and payloads pass at
that suite's receiver.

Community SDKs, including Go SDKs, MAY be test targets but MUST NOT override an
official source merely because their behavior differs.

## Provenance hierarchy

When sources conflict, the chosen behavior SHOULD follow:

1. Repeatable observation from a controlled Slack developer workspace.
2. Current official Slack documentation.
3. Consensus across current official SDK releases.
4. A single current official SDK release.
5. The archived published OpenAPI/AsyncAPI source.
6. An explicit local compatibility decision.

The ledger MUST record every conflict, all evidence, the selected behavior, and
the reason. Upstream files MUST remain byte-for-byte copies; corrections belong
in versioned overlays.

## Normalized catalog

Source tooling MUST extract, where present:

- method and protocol names;
- HTTP method and path;
- query, form, multipart, and JSON parameters;
- requiredness, types, defaults, aliases, and deprecations;
- token types and scopes;
- response objects, unions, warnings, and errors;
- cursor and legacy pagination;
- rate-limit and retry headers;
- file-upload sequences;
- event and interactive envelopes;
- OAuth, webhook signing, and Socket Mode behavior; and
- source location and revision for each fact.

SDK convenience behavior MUST be distinguished from server obligations. A
client-side retry loop, for example, implies required server status/headers but
is not server logic to reproduce.

## HTTP behavior

- Web API methods MUST be served beneath `/api/{method}`.
- Every Web API method MUST accept both `GET` and `POST`, as Slack does; the
  verb the OpenAPI snapshot lists is not a restriction, because official SDKs
  differ in which they send. An unknown method name, and any other verb,
  answers the `unknown_method` envelope.
- Routes outside `/api/` (external upload URLs, incoming and trigger
  webhooks, public file and photo URLs) MUST be reachable in the default,
  rate-limited deployment configuration; the Web API limiter fronts `/api/`
  only.
- The decoder MUST accept the request encodings supported by the selected
  contract, including query, URL-encoded form, multipart, and JSON.
- JSON, form, and query parameters MUST NOT be combined where Slack forbids it.
- Authentication MUST support the selected bearer-token and legacy token
  placements.
- Mutating requests MAY carry an explicit `Idempotency-Key`; when present, the
  same committed result MUST be returned for a retry instead of creating a
  second mutation.
- JSON Web API responses MUST contain a top-level `ok` boolean unless a pinned
  contract explicitly defines a different response.
- HTTP 500 MUST be reserved for an unhandled exception. Handled validation,
  authorization, persistence, dependency, and lifecycle errors MUST use their
  selected non-500 status and Slack-shaped error response. If the pinned Slack
  contract intentionally returns 500 for a specific condition, that observed
  contract is authoritative and MUST be recorded in the compatibility ledger.
- Documented warnings, errors, metadata, and pagination cursors MUST retain
  compatible names and types.
- Unknown request fields and unknown response fields MUST follow the behavior
  selected in the compatibility ledger.

List responses MUST normalize page parameters at the wire boundary and MUST
use bounded pages or a streaming representation. Implementations MUST NOT
materialize an unbounded collection merely to serialize a response.

## SDK suites

Test projects MUST exercise exact pinned releases of official SDKs. Each suite
MUST target the local API base URL directly or through a transparent test proxy
and MUST cover, as applicable:

- request encoding;
- authentication;
- scalar, array, nested, null, and boolean arguments;
- response and error decoding;
- pagination helpers;
- rate limiting and retry behavior;
- uploads and download metadata;
- events, OAuth, webhooks, and interactivity; and
- Socket Mode envelopes.

An operation cannot be `sdk-compatible` until every applicable pinned official
SDK suite passes.

## Cold-wake behavior

The activator MAY delay an API request while waking the stack, but MUST preserve
method, path, headers, and body exactly except for hop-by-hop transport headers.
It MUST NOT replay a non-idempotent request more than once unless the request is
protected by a durable idempotency record.

If the activation deadline or buffer limit is exceeded, the activator MUST
return HTTP 503 with `Retry-After`. The Slack-shaped response and its documented
compatibility deviation MUST be recorded in the ledger; the system MUST NOT
pretend that unspecified cold-start behavior came from Slack.

## Differential verification

Tests MAY submit equivalent calls to a disposable Slack workspace. Comparison
MUST normalize tokens, IDs, timestamps, request IDs, hostnames, and other
volatile fields. Captured private content and credentials MUST NOT be committed.
