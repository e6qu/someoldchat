# Files

SameOldChat stores file metadata in the selected durable store and file bytes
in the configured blob store. The application does not treat a process-local
buffer as file storage.

## External upload lifecycle

The Slack external upload flow has three explicit operations:

1. `files.getUploadURLExternal` creates a durable upload ticket and returns an
   opaque upload URL together with the file identifier. The identifier is
   minted once, before any bytes exist, and completion keeps it, so a client
   can record it up front.
2. The client sends the declared number of bytes to that URL. The server
   streams the request into the blob store and records the ticket as uploaded
   only after the blob store accepts the complete object.
3. `files.completeUploadExternal` atomically changes the ticket to completed,
   creates the durable file metadata, and appends the file-created event.

An expired or already completed ticket fails. The file metadata and ticket
transition commit together, so a crash before completion leaves an uploaded
ticket that can be completed again.

### Capability URLs

Three server-minted paths are unauthenticated capability URLs, where
possession of the token is the authorization:

- `POST /internal/files/external/{upload}`, the external upload target;
- `GET /files/public/{token}`, a public file download; and
- `GET /users/{workspace}/{user}/photo/{token}`, a user avatar.

A CDN, WAF, or access-log configuration in front of the application must treat
these whole paths as secrets and must not log them.

The upload URL answers with a plain HTTP status, as Slack's does, because the
official SDKs judge the upload by the status alone: 200 when the bytes are
stored, 400 when the body is malformed or its length disagrees with the
ticket, 404 when the ticket is unknown, expired, or already used, and 503 when
the blob store is unavailable. Completing a ticket whose bytes never arrived
is `file_not_found`. When the caller names no `mime_type`, the media type is
inferred from the file name's extension (a fixed table in
`internal/domain/filetype.go`, not the host's MIME database), and `filetype`
and `pretty_type` are derived from the same table.

## Absolute URLs

Every URL a file object carries — `url_private`, `url_private_download`,
`permalink`, `permalink_public` — and the `upload_url` of
`files.getUploadURLExternal` is absolute, because SDKs and apps fetch them
verbatim. The origin is the configured `-auth-public-url` when one is set;
otherwise it is the request's own: `https` for a TLS connection or an
`X-Forwarded-Proto` of exactly `http` or `https` (any other value is ignored),
and the `Host` header. A deployment behind a proxy should set the public URL.
The one place that decides this is `internal/api/slack/origin.go`.

The file objects inside Events API, Socket Mode and RTM payloads are the same
object `files.info` returns, built on the configured public URL. An event has
no request to take an origin from, so a deployment without `-auth-public-url`
delivers them origin-relative; see [Public URL](operations.md#public-url).

## Shares

`files.info` reports `shares` as Slack does — `{"public"|"private": {channel:
[{ts, thread_ts?, channel_name, team_id, share_user_id}]}}` — one entry per
live message that carries the file, limited to public channels and the
conversations the reader belongs to. Other file objects carry only `channels`,
so a list read does not pay a message join per file.

## Listing and deletion

`files.list` returns files newest first (`created` descending, then id), so a
page is the same page on every storage profile. `files.delete` leaves a
tombstone — `{"id": "F…", "mode": "tombstone"}` — in every message that shared
the file and announces the deletion as `file_deleted` in the same commit.

`files.completeUploadExternal` accepts either a single `upload_id` or a `files`
array of several completions in one request. A completion can include channel
identifiers, an initial comment, Block Kit blocks, and a `thread_ts`. The channel
relation is committed with every file's metadata. The comment or blocks are
published once as an idempotent message for each shared channel; when both are
supplied, the initial comment takes precedence as in the Slack method contract.
The message does not contain a fabricated file attachment reference.

If publication is interrupted after completion, retrying the same completion
request reads the durable channel relation and retries only the missing
idempotent messages. The file is not created again. A retry cannot change the
durable channel relation by supplying a different channel list.

## Process boundary

In local composition the HTTP handler calls the chat service directly. In
distributed composition the same service methods cross the generated gRPC
boundary. Byte transfer uses a client-streaming gRPC method and does not load the object
into application memory.

The storage state machine is implemented by the in-memory development store
and the shared SQL store (`internal/store/sqlstore`) used by SQLite,
PostgreSQL, and dqlite. The
blob store remains an explicit deployment choice; a disabled blob store fails
file operations instead of reporting an empty file collection.

## Upstream contract

Slack documents `files.upload` as a legacy operation and directs clients to
the external upload sequence. See the official references for
[`files.getUploadURLExternal`](https://docs.slack.dev/reference/methods/files.getUploadURLExternal/),
[`files.completeUploadExternal`](https://docs.slack.dev/reference/methods/files.completeUploadExternal/),
and [`files.upload`](https://docs.slack.dev/reference/methods/files.upload/). The local
compatibility decisions and evidence status are recorded in the
[compatibility ledger](../specs/compatibility.yaml).

Related implementation boundaries are described in the
[separable module architecture](modules.md) and [operations guide](operations.md).
