import { defineConfig } from '@playwright/test';

// The probe runs against the already-running webkit fixture on 18082, one
// browser, no retries, so a single crash is a single, legible result.
export default defineConfig({
  testDir: '.',
  testMatch: /\.probe\.mjs$/,
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: { baseURL: 'http://127.0.0.1:18082', trace: 'retain-on-failure' },
  projects: [{ name: 'webkit', use: { browserName: 'webkit' } }],
  webServer: [
    {
      command: 'cd ../../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18082 -chat-mode local -store memory -blob-dir "$PWD/.cache/probe-blobs-webkit" -bootstrap-admin-email browser-admin@localhost.test -api-token xoxb-browser -session-token browser-session -api-rate-limit=false',
      url: 'http://127.0.0.1:18082/healthz',
      reuseExistingServer: false,
      timeout: 120000,
    },
  ],
});
