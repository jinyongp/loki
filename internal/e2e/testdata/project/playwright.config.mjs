import { defineConfig } from "@playwright/test";

const executablePath = process.env.LOKI_CHROMIUM_PATH;
if (!executablePath) {
  throw new Error("LOKI_CHROMIUM_PATH is required");
}

export default defineConfig({
  workers: 1,
  retries: 0,
  reporter: "line",
  use: {
    headless: true,
    launchOptions: {
      executablePath,
      chromiumSandbox: false,
    },
  },
});
