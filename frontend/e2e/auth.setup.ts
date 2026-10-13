import { execFileSync } from "node:child_process";
import { test as setup, expect } from "@playwright/test";
import { BACKEND_DIR, E2E_BACKEND_ENV, authFile, keycloakLogin, type E2EUser } from "./support";

function createInvite(admin: boolean): string {
  const stdout = execFileSync("./.e2e/server", admin ? ["invite", "--admin"] : ["invite"], {
    cwd: BACKEND_DIR,
    env: { ...process.env, ...E2E_BACKEND_ENV },
    encoding: "utf8",
  });
  const url = stdout.match(/^http:\/\/localhost:5174\/api\/auth\/login\?invite=\S+$/m)?.[0];
  if (!url) throw new Error(`invite command printed no :5174 invite link: ${stdout}`);
  return url;
}

const users: { name: E2EUser; admin: boolean }[] = [
  { name: "alice", admin: true },
  { name: "bob", admin: false },
];

for (const { name, admin } of users) {
  setup(`sign in ${name} through an invite`, async ({ browser }) => {
    const invite = createInvite(admin);
    const context = await browser.newContext();
    try {
      const page = await context.newPage();
      await page.goto(invite);
      await keycloakLogin(page, name);
      await expect(page.locator(".leaflet-container")).toBeVisible();
      await expect(page.getByRole("button", { name: "Logout" })).toBeVisible();
      await context.storageState({ path: authFile(name) });
    } finally {
      await context.close();
    }
  });
}
