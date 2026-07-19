import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './specs',
  fullyParallel: false,
  workers: 1,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:18080',
    browserName: 'chromium',
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
      ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
      : {},
    trace: 'retain-on-failure',
  },
  webServer: {
    command: 'cd ../.. && GOCACHE="$PWD/.cache/go-build" go run ./cmd/server -addr 127.0.0.1:18080 -chat-mode local -store memory -api-token xoxb-browser -session-token browser-session',
    url: 'http://127.0.0.1:18080/healthz',
    timeout: 120_000,
    reuseExistingServer: false,
  },
});
