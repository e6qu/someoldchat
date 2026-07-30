# Files and media

## FILE-01 — Stage and upload files with a message

**Entry points:** Composer attachment control, drag and drop, paste where Slack
supports it, and supported system share/file-picker paths.

The member can select or drop up to Slack's current count and size limits,
inspect name/type/size and a safe preview, reorder staged files where Slack
allows, add a message, remove individual files, and add descriptions/alt text.
Selection alone does not upload or post a message unless Slack currently starts
the upload early and the UI exposes its cancellable state.

Send produces one hosted file per accepted file and one Slack-compatible
message projection without duplicate `file_share` history. Progress,
cancellation, retry, partial batch failure, unsupported type, too-large,
malware/security rejection, quota, lost permission, and expired external-upload
URL preserve an honest recoverable state.

## FILE-02 — Upload through the external Web API flow

Current official SDKs perform Slack's external upload sequence: request an
upload URL, transfer exact bytes, and complete the upload. Completion is
idempotent and verifies size/ownership/state. A completed channel share becomes
one history message and appropriate file event; an uncompleted or failed
transfer never becomes a successful file.

Legacy `files.upload` behavior is retained only at its explicitly recorded
compatibility level and MUST not be used as evidence for the current external
flow.

## FILE-03 — Render and inspect a file

File messages expose safe metadata, preview/thumbnail where supported, uploader,
description/alt text, share destination, and actions. Preview failure falls
back to metadata/download rather than a broken or unsafe embed. Images,
audio/video, text, PDF, archives, unknown binaries, remote files, deleted
files, and app-owned files follow Slack's distinguishable states.

## FILE-04 — Browse and search files

Slack's Files/browse surface lists only visible files, supports current filters
and search, paginates stably, and opens the file or containing message. File
results in global search agree with this visibility. Empty, indexing, and
failure states remain distinct.

Visibility is evaluated before totals and page boundaries: the uploader can
see an unshared file, every workspace member can see a file shared to a public
channel, and only members of each private channel/DM can discover its share.
The same decision governs Files browse, message/file/combined search, file
metadata, previews, and byte download. Search covers safe name/title text,
uploader, conversation, date, and Slack-supported file-type refinements and
preserves them in a reloadable URL. Sorting is deterministic for equal
timestamps, and a later permission loss makes a previously returned link fail
closed without revealing whether the object still exists.

## FILE-05 — Download, share, and copy a file link

Download requires an authorized current session/token and streams the exact
bytes with safe content type/disposition, range behavior, and filename.
Sharing to another conversation checks visibility and creates Slack's expected
share projection. A public link, where supported, is an explicit revocable
capability and never appears merely by copying an authenticated URL.

## FILE-06 — Delete and unshare a file

An authorized uploader/admin can delete according to retention policy and
Slack's confirmation. Delete/unshare updates every message, search result,
preview, public link, and event projection consistently. Blob cleanup happens
only after durable references allow it and is recoverable/reconciled. Removing
one share does not delete other shares unless Slack's delete action does.

## FILE-07 — Use remote files

Apps can add, update, share, and remove remote-file metadata through Slack's
current API without SameOldChat pretending to host the remote bytes. External
IDs are app/workspace isolated; preview/indexable content and links are
sanitized; sharing follows conversation visibility; removal reconciles search
and message projections.

## Evidence

- Real browser uploads cover chooser, drag/drop, paste, multiple files,
  progress/cancel/retry, preview, description, download, share, delete, and
  narrow layout.
- Official Node, Python, and Java SDKs execute the current external upload
  protocol against real HTTP transfer, plus remote-file methods where exposed.
- Persistence qualification verifies metadata/blob atomicity and reconciliation
  across SQLite, PostgreSQL, and dqlite.
- Differential tests compare count/size limits, message projection, errors, and
  file events in a dedicated Slack workspace.
- Memory and shared SQL persistence apply viewer visibility before file-list
  and file-search pagination; SQL persists folded file name/title columns for
  Unicode-insensitive matching after reopen. Generated gRPC parity tests carry
  the viewer, filters, totals, and order. Official Node, Python, and Java SDK
  qualification invokes both `search.files` and legacy combined `search.all`,
  and browser qualification searches a real hosted upload through the Files
  result type before following its authenticated link.

## Journey-source map

| Journey | Official source | Behavior established |
| --- | --- | --- |
| FILE-01 | [Add files to Slack](https://slack.com/help/articles/201330736-Add-files-to-Slack) | Slack stages up to ten files, previews them, and sends them with a message. |
| FILE-02 | [Upload files to Slack](https://docs.slack.dev/messaging/working-with-files/) | Current app uploads use the external upload URL and completion sequence. |
| FILE-03 | [Add files to Slack](https://slack.com/help/articles/201330736-Add-files-to-Slack) | Slack renders safe file previews subject to documented type and size limits. |
| FILE-04 | [Add files to Slack](https://slack.com/help/articles/201330736-Add-files-to-Slack) | Files has a visible browse surface and conversation-specific Files and links tab. |
| FILE-05 | [Share files in Slack](https://slack.com/help/articles/204399343-Share-files-in-Slack) | Files can be shared internally or through an explicit external link. |
| FILE-06 | [Share files in Slack](https://slack.com/help/articles/204399343-Share-files-in-Slack) | File shares and external links have explicit removal and access consequences. |
| FILE-07 | [Upload files to Slack](https://docs.slack.dev/messaging/working-with-files/) | Slack distinguishes hosted upload bytes from app-managed remote file metadata. |

Sources checked 2026-07-29:

- [Add files to Slack](https://slack.com/help/articles/201330736-Add-files-to-Slack)
- [Share files in Slack](https://slack.com/help/articles/204399343-Share-files-in-Slack)
- [Upload files to Slack](https://docs.slack.dev/messaging/working-with-files/)
