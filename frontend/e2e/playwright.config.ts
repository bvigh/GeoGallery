import { defineConfig, devices } from "@playwright/test";
import { BACKEND_URL, BASE_URL, E2E_BACKEND_ENV } from "./support";

export default defineConfig({
  testDir: ".",
  fullyParallel: true,
  reporter: "list",
  webServer: [
    {
      command: "go build -o ./.e2e/server ./cmd/server && ../scripts/e2e-reset-db.sh && ./.e2e/server",
      cwd: "../../backend",
      url: `${BACKEND_URL}/api/health`,
      reuseExistingServer: false,
      timeout: 180_000,
      env: E2E_BACKEND_ENV,
    },
    {
      command: "npx vite --port 5174 --strictPort",
      cwd: "..",
      url: BASE_URL,
      reuseExistingServer: false,
      timeout: 180_000,
      env: { VITE_API_TARGET: BACKEND_URL },
    },
  ],
  use: {
    baseURL: BASE_URL,
    ...devices["Desktop Chrome"],
    trace: "retain-on-failure",
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.ts$/ },
    { name: "e2e", testMatch: /.*\.e2e\.ts$/, dependencies: ["setup"] },
  ],
});
