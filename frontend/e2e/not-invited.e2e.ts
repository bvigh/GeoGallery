import { test, expect } from "@playwright/test";
import { keycloakLogin } from "./support";

test("an uninvited account is turned away with a way to switch accounts", async ({ browser }) => {
  const context = await browser.newContext();
  try {
    const page = await context.newPage();
    await page.goto("/");
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await keycloakLogin(page, "carol");

    await expect(page).toHaveURL(/\/\?auth_error=not_invited$/);
    await expect(page.getByRole("alert")).toContainText("This account isn't invited.");
    await expect(page.getByRole("link", { name: "Sign in with a different account" })).toHaveAttribute(
      "href",
      "/api/auth/login?prompt=login",
    );
  } finally {
    await context.close();
  }
});
