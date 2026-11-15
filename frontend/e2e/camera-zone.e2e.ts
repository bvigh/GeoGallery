import { test, expect } from "@playwright/test";
import { authFile } from "./support";

// Bob uploads nothing in any spec running beside this one, so his zone dates no file here.
test.use({ storageState: authFile("bob") });

test("a camera time zone set in Settings is kept, and is the Upload page's default", async ({ page }) => {
  try {
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Camera time zone" })).toBeVisible();
    const zone = page.getByLabel("Zone", { exact: true });
    await expect(zone.locator("option").first()).toHaveText(/^Server default \(/);
    await zone.selectOption("Asia/Tokyo");
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(page.getByRole("status")).toHaveText("Saved.");

    await page.reload();
    await expect(page.getByLabel("Zone", { exact: true })).toHaveValue("Asia/Tokyo");

    await page.getByRole("button", { name: "Upload", exact: true }).click();
    const uploadZone = page.getByLabel("Camera time zone");
    await expect(uploadZone.locator("option").first()).toHaveText("Default: your camera zone, Asia/Tokyo");
    await expect(uploadZone).toHaveValue("");
  } finally {
    // Back to the server's zone, whatever happened above.
    const reset = await page.request.patch("/api/me/settings", { data: { camera_timezone: null } });
    expect(reset.status()).toBe(200);
  }
});
