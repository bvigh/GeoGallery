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

// The real summary through the Vite proxy and the admin route. Bob's library is empty for the whole
// e2e project; alice's may already hold the upload spec's photo, which runs beside this one.
test("the admin counts every user's storage use", async ({ browser }) => {
  const alice = await browser.newContext({ storageState: authFile("alice") });
  try {
    const page = await alice.newPage();
    await page.goto("/admin");
    const count = page.getByRole("button", { name: "Show storage use" });
    await count.click();
    const users = page.getByRole("table", { name: "Storage per user" });
    await expect(users).toBeVisible();
    await expect(page.getByRole("button", { name: "Count again" })).toBeFocused();
    await expect(page.getByText(/^In all: \d+ items?, /)).toBeVisible();

    const bob = users.getByRole("row").filter({ has: page.getByRole("cell", { name: /^Bob Test/ }) });
    await expect(bob.getByRole("cell")).toHaveText(["Bob Test0 items · 0 B · 0 track points", "0", "0 B", "0"]);
    await expect(users.getByRole("row").filter({ has: page.getByRole("cell", { name: /^Alice Test/ }) })).toHaveCount(1);
  } finally {
    await alice.close();
  }
});
