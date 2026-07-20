# Files

SameOldChat stores file metadata in the selected durable store and file bytes
in the configured blob store. The application does not treat a process-local
buffer as file storage.

## External upload lifecycle

The Slack external upload flow has three explicit operations:

1. `files.getUploadURLExternal` creates a durable upload ticket and returns an
   opaque upload URL.
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
[`files.getUploadURLExternal`](https://api.slack.com/methods/files.getUploadURLExternal),
[`files.completeUploadExternal`](https://api.slack.com/methods/files.completeUploadExternal),
and [`files.upload`](https://api.slack.com/methods/files.upload). The local
compatibility decisions and evidence status are recorded in the
[compatibility ledger](../specs/compatibility.yaml).

Related implementation boundaries are described in the
[separable module architecture](modules.md) and [operations guide](operations.md).
