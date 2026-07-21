# Browser qualification

This suite runs the seeded local application in Chromium and exercises the
browser journey that server-side tests cannot observe: session-authenticated
workspace entry, message posting, workspace search, theme switching, and
navigation to workspace members. It also signs out through the application UI,
asserts the application-owned signed-out destination remains terminal across a
reload, exposes an explicit **Sign in with Shauth** control, and verifies the
revoked session cannot reopen a protected page.

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

The separate `make shauth-sso-qualification` gate requires
`SHAUTH_SOURCE_DIR` to point at Shauth commit
`0fda680cba964e5768ed75a9c3e5b7230c418ca6`. It uses the same pinned Playwright
installation to exercise two real SameOldChat relying parties against real
Shauth, Ory Hydra, and PostgreSQL services. The two applications use distinct
databases and dynamically allocated loopback ports, while `.localhost` origins
preserve secure relying-party origin behavior without fixed host-port
collisions.

The browser qualification is separate from the official Slack SDK suites in
[`../official-sdk-qualification`](../official-sdk-qualification/README.md).
The repository's build and release checks are documented in
[`../../README.md`](../../README.md).
