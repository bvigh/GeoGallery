import { test, expect } from "@playwright/test";

test("an anonymous visitor sees the sign-in screen", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("Sign in to view your gallery.")).toBeVisible();
  await expect(page.getByRole("link", { name: "Sign in", exact: true })).toHaveAttribute("href", "/api/auth/login");
  await expect(page.getByRole("button", { name: "Logout" })).toHaveCount(0);
});
