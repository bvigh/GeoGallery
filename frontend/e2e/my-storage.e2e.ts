import { test, expect } from "@playwright/test";
import { authFile } from "./support";

test.use({ storageState: authFile("bob") });

// The caller's own storage through the Vite proxy and the real route. Bob's library is empty for
// the whole e2e project.
test("a user's Settings show their own storage use", async ({ page }) => {
  await page.goto("/settings");
  const storage = page.getByRole("region", { name: "Storage" });
  await expect(storage.getByText("You have no items yet.")).toBeVisible();
  await expect(storage.getByText("Could not load your storage use.")).toHaveCount(0);
});
