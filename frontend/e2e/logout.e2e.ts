import { test, expect } from "@playwright/test";
import { keycloakLogin } from "./support";

test("logout ends the session and the IdP session without a confirmation page", async ({ browser }) => {
  const context = await browser.newContext();
  try {
    const page = await context.newPage();
    await page.goto("/");
    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await keycloakLogin(page, "bob");
    await expect(page.getByRole("button", { name: "Logout" })).toBeVisible();

    const committed: string[] = [];
    const idpLogouts: URL[] = [];
    page.on("framenavigated", (frame) => {
      if (frame === page.mainFrame()) committed.push(frame.url());
    });
    page.on("request", (req) => {
      const url = new URL(req.url());
      if (url.port === "8180" && url.pathname.endsWith("/protocol/openid-connect/logout")) idpLogouts.push(url);
    });
    await page.getByRole("button", { name: "Logout" }).click();

    await expect(page.getByText("Sign in to view your gallery.")).toBeVisible();
    expect(new URL(page.url()).origin).toBe("http://localhost:5174");
    expect(committed.filter((u) => new URL(u).port === "8180")).toEqual([]);
    expect(idpLogouts).toHaveLength(1);
    expect(idpLogouts[0].searchParams.get("id_token_hint")).toBeTruthy();
    expect(idpLogouts[0].searchParams.get("post_logout_redirect_uri")).toBe("http://localhost:5174/");

    await page.reload();
    await expect(page.getByText("Sign in to view your gallery.")).toBeVisible();
    expect((await page.request.get("/api/auth/me")).status()).toBe(401);

    await page.getByRole("link", { name: "Sign in", exact: true }).click();
    await expect(page.locator("#username")).toBeVisible();
  } finally {
    await context.close();
  }
});
