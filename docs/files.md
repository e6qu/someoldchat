# Files

SameOldChat stores file metadata in the selected durable store and file bytes
in the configured blob store. The application does not treat a process-local
buffer as file storage.

## External upload lifecycle

The Slack external upload flow has three explicit operations:

1. `files.getUploadURLExternal` creates a durable upload ticket and returns an
   opaque upload URL together with the identifier the finished file will carry.
   That identifier is minted once, before any bytes exist, so a client can
   record it and reference the file by it after completion; completion never
   mints a new one.
2. The client sends the declared number of bytes to that URL. The server
   streams the request into the blob store and records the ticket as uploaded
   only after the blob store accepts the complete object.
3. `files.completeUploadExternal` atomically changes the ticket to completed,
   creates the durable file metadata, and appends the file-created event.

An expired or already completed ticket fails. A process crash before the
completion transaction leaves an uploaded ticket that can be completed again;
the file metadata and ticket transition commit together. The upload URL is an
opaque bearer capability, so operators must protect the application endpoint
and avoid logging request URLs.

The current implementation accepts one completed file per request. Channel
sharing, initial comments, and Block Kit publication associated with external
completion are not silently inferred; callers must use the existing message
and file-sharing operations until those fields have an explicit implementation.
The current implementation accepts one completed file per request. A completion
can include channel identifiers, an initial comment, and Block Kit blocks. The
channel relation is committed with the file metadata. The comment and blocks
are published as an idempotent message for each shared channel; the message
does not contain a fabricated file attachment reference.
The current implementation accepts one or more completed files per request. A
completion can include channel identifiers, an initial comment, and Block Kit
blocks. The channel relation is committed with every file's metadata. The
comment or blocks are published once as an idempotent message for each shared
channel; when both are supplied, the initial comment takes precedence as in
the Slack method contract. The message does not contain a fabricated file
attachment reference.

If publication is interrupted after completion, retrying the same completion
request reads the durable channel relation and retries only the missing
idempotent messages. The file is not created again. A retry cannot change the
durable channel relation by supplying a different channel list.

## Process boundary

In the monolith, the HTTP handler calls the local chat service directly. In
distributed mode, the same service methods cross the generated gRPC boundary.
Byte transfer uses a client-streaming gRPC method and does not load the object
into application memory.

The storage state machine is implemented by the in-memory development store
and the SQL store used by SQLite, PostgreSQL compatibility, and dqlite. The
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
