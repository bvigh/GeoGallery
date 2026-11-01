import { test, expect } from "@playwright/test";
import { authFile } from "./support";

test("only the admin sees the Admin tab", async ({ browser }) => {
  const bob = await browser.newContext({ storageState: authFile("bob") });
  try {
    const page = await bob.newPage();
    await page.goto("/");
    await expect(page.getByRole("button", { name: "Settings", exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: "Admin", exact: true })).toHaveCount(0);
  } finally {
    await bob.close();
  }
});

test("the admin creates an invite link", async ({ browser }) => {
  const alice = await browser.newContext({ storageState: authFile("alice") });
  try {
    const page = await alice.newPage();
    await page.goto("/");
    await page.getByRole("button", { name: "Admin", exact: true }).click();
    await page.getByRole("button", { name: "Create invite" }).click();
    await expect(page.getByLabel("Invite link")).toHaveValue(
      /^http:\/\/localhost:5174\/api\/auth\/login\?invite=[A-Za-z0-9_-]+$/,
    );
  } finally {
    await alice.close();
  }
});
