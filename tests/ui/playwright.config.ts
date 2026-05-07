import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  // Always use 1 worker: tests share a single fakedevice and race if
  // run concurrently. Parallel execution would require per-test device
  // isolation which is not implemented.
  workers: 1,
  reporter: [["list"]],
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  use: {
    headless: true,
    // baseURL is set by global-setup after the daemon starts.
    // Smoke tests use page.goto("/") which resolves against this base.
    baseURL: process.env.BREEZYD_URL,
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
});
