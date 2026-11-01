import { test, expect } from "@playwright/test";
import { authFile } from "./support";

test.use({ storageState: authFile("bob") });

test("a new user starts with an empty map and no game rounds", async ({ page }) => {
  const media = page.waitForResponse((r) => new URL(r.url()).pathname === "/api/media");
  await page.goto("/");
  expect((await media).status()).toBe(200);
  expect(await (await media).json()).toEqual([]);
  await expect(page.locator(".leaflet-container")).toBeVisible();
  await expect(page.locator(".leaflet-marker-icon")).toHaveCount(0);

  await page.getByRole("button", { name: "Game", exact: true }).click();
  await page.getByRole("button", { name: "Start Round" }).click();
  await expect(page.getByText("No geolocated media yet")).toBeVisible();
});
