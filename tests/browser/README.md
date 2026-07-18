# Browser qualification

This suite runs the seeded local application in Chromium and exercises the
browser journey that server-side tests cannot observe: session-authenticated
workspace entry, message posting, workspace search, theme switching, and
navigation to workspace members.

Run it from the repository root:

```sh
make browser-qualification
```

The suite uses the pinned Playwright version in `package.json` and the lock
file. It starts `cmd/server` with the local in-memory store and a disposable
browser session. It does not test a production deployment or use a remote
authorization provider.

The browser qualification is separate from the official Slack SDK suites in
[`../official-sdk-qualification`](../official-sdk-qualification/README.md).
The repository's build and release checks are documented in
[`../../README.md`](../../README.md).
