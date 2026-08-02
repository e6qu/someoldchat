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
      use: {
        baseURL: 'http://127.0.0.1:18080',
        browserName: 'chromium',
        launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
          ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
          : {},
      },
    },
    { name: 'firefox', use: { baseURL: 'http://127.0.0.1:18081', browserName: 'firefox' } },
    { name: 'webkit', use: { baseURL: 'http://127.0.0.1:18082', browserName: 'webkit' } },
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
  ],
});
