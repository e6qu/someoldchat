# Browser qualification

This suite runs the seeded local application in Chromium and exercises the
browser journey that server-side tests cannot observe: session-authenticated
workspace entry, message posting, workspace search, theme switching, and
navigation to workspace members. It also signs out through the application UI,
asserts the application-owned signed-out destination remains terminal across a
reload, and verifies the revoked session cannot reopen a protected page.

Run it from the repository root:

```sh
make browser-qualification
```

`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` may point at an already-installed
Chromium-compatible executable. This is an explicit browser coordinate for
developer workstations; CI continues to install and run the lockfile-matched
Playwright Chromium build.

The suite uses the pinned Playwright version in `package.json` and the lock
file. It starts `cmd/server` with the local in-memory store and a disposable
browser session. It does not test a production deployment or use a remote
authorization provider.

The browser qualification is separate from the official Slack SDK suites in
[`../official-sdk-qualification`](../official-sdk-qualification/README.md).
The repository's build and release checks are documented in
[`../../README.md`](../../README.md).
