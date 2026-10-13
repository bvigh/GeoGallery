import { test, expect, request } from "@playwright/test";
import { BASE_URL, authFile } from "./support";

test.use({ storageState: authFile("bob") });

test("an API token can import and read, cannot manage tokens or delete, and dies on revoke", async ({ page }) => {
  const start = Date.now();
  const name = `e2e-${start}`;

  await page.goto("/");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await page.getByRole("button", { name: "Create token" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Name").fill(name);
  await dialog.getByRole("button", { name: "Create token" }).click();
  const tokenField = dialog.getByLabel("New API token");
  await expect(tokenField).toHaveValue(/^gg_pat_[A-Za-z0-9_-]{43}$/);
  const token = await tokenField.inputValue();
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(dialog).toHaveCount(0);

  const api = await request.newContext({
    baseURL: BASE_URL,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
  });
  try {
    const imported = await api.post("/api/gps-tracks/import", {
      data: [
        { recorded_at: new Date(start).toISOString(), lat: 47.4979, lng: 19.0402, source: "e2e" },
        { recorded_at: new Date(start + 7_200_000).toISOString(), lat: 47.5, lng: 19.05, source: "e2e" },
      ],
    });
    expect(imported.status()).toBe(200);
    expect(await imported.json()).toMatchObject({ inserted: 2 });

    expect((await api.get("/api/media")).status()).toBe(200);
    expect((await api.post("/api/tokens", { data: { name: "minted" } })).status()).toBe(403);
    expect((await api.delete("/api/media/00000000-0000-0000-0000-000000000000")).status()).toBe(403);

    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: `Revoke ${name}`, exact: true }).click();
    await expect(page.getByRole("row", { name: new RegExp(name) })).toContainText("revoked");

    expect((await api.get("/api/media")).status()).toBe(401);
  } finally {
    await api.dispose();
  }
});
