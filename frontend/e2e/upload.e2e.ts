import path from "node:path";
import { test, expect } from "@playwright/test";
import { E2E_DIR, authFile } from "./support";

test.use({ storageState: authFile("alice") });

test("an uploaded geotagged photo appears as a marker on the map", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("button", { name: "Upload", exact: true }).click();
  await page
    .locator('input[type="file"][accept^="image/"]')
    .setInputFiles(path.join(E2E_DIR, "fixtures", "gps.jpg"));
  await page.getByRole("main").getByRole("button", { name: "Upload", exact: true }).click();
  await expect(page.getByText("Uploaded 1 file(s)")).toBeVisible();

  await page.getByRole("button", { name: "Map", exact: true }).click();
  await expect(page.locator(".leaflet-marker-pane .leaflet-marker-icon")).toHaveCount(1);
});
