import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './specs',
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:18080',
    trace: 'retain-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      testIgnore: /administration\.spec\.mjs/,
      use: {
        baseURL: 'http://127.0.0.1:18080',
        browserName: 'chromium',
        launchOptions: {
          // Synthetic devices, so a huddle can connect without a microphone
          // and without a permission prompt no test could answer.
          args: ['--use-fake-device-for-media-stream', '--use-fake-ui-for-media-stream'],
          ...(process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
            ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
            : {}),
        },
      },
    },
    {
      name: 'firefox',
      testIgnore: /administration\.spec\.mjs/,
      use: {
        baseURL: 'http://127.0.0.1:18081',
        browserName: 'firefox',
        launchOptions: {
          firefoxUserPrefs: {
            'media.navigator.streams.fake': true,
            'media.navigator.permission.disabled': true,
          },
        },
      },
    },
    // WebKit has no synthetic capture device in Playwright, so the media half
    // of HUDDLE-02 cannot run there. The journey records that rather than
    // pretending the browser was covered.
    { name: 'webkit', testIgnore: /administration\.spec\.mjs/, use: { baseURL: 'http://127.0.0.1:18082', browserName: 'webkit' } },
    // Administration runs against its own server because the escalation is the
    // point: -session-admin gives the shared session control-plane scopes, and
    // the other three projects must keep asserting what a plain member sees.
    // One engine, because these surfaces are server-rendered forms with no
    // engine-specific behaviour, and a fourth server per engine would triple
    // the harness for no additional evidence.
    {
      name: 'chromium-admin',
      testMatch: /administration\.spec\.mjs/,
      use: {
        baseURL: 'http://127.0.0.1:18083',
        browserName: 'chromium',
        launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
          ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
          : {},
      },
    },
  ],
  webServer: [
    {
      command: 'cd ../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18080 -chat-mode local -store memory -blob-dir "$PWD/.cache/browser-blobs-chromium" -bootstrap-admin-email browser-admin@localhost.test -api-token xoxb-browser -session-token browser-session -api-rate-limit=false',
      url: 'http://127.0.0.1:18080/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
    },
    {
      command: 'cd ../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18081 -chat-mode local -store memory -blob-dir "$PWD/.cache/browser-blobs-firefox" -bootstrap-admin-email browser-admin@localhost.test -api-token xoxb-browser -session-token browser-session -api-rate-limit=false',
      url: 'http://127.0.0.1:18081/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
    },
    {
      command: 'cd ../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18082 -chat-mode local -store memory -blob-dir "$PWD/.cache/browser-blobs-webkit" -bootstrap-admin-email browser-admin@localhost.test -api-token xoxb-browser -session-token browser-session -api-rate-limit=false',
      url: 'http://127.0.0.1:18082/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
    },
    {
      command: 'cd ../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18083 -chat-mode local -store memory -blob-dir "$PWD/.cache/browser-blobs-admin" -bootstrap-admin-email browser-admin@localhost.test -api-token xoxb-browser -session-token browser-session -session-admin -api-rate-limit=false',
      url: 'http://127.0.0.1:18083/healthz',
      timeout: 120_000,
      reuseExistingServer: false,
    },
  ],
});
