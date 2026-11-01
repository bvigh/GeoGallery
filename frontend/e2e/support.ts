import path from "node:path";
import { fileURLToPath } from "node:url";
import { expect, type Page } from "@playwright/test";

export const E2E_DIR = path.dirname(fileURLToPath(import.meta.url));
export const BACKEND_DIR = path.resolve(E2E_DIR, "../../backend");
export const BASE_URL = "http://localhost:5174";
export const BACKEND_URL = "http://localhost:8091";

export const E2E_BACKEND_ENV = {
  SERVER_PORT: "8091",
  PUBLIC_URL: BASE_URL,
  DATABASE_URL: "postgres://geogallery:geogallery@localhost:5432/geogallery_e2e?sslmode=disable",
  OIDC_ISSUER_URL: "http://localhost:8180/realms/geogallery",
  OIDC_DISCOVERY_URL: "http://localhost:8180/realms/geogallery",
  OIDC_CLIENT_ID: "geogallery",
  OIDC_CLIENT_SECRET: "geogallery-dev-secret",
  LOG_LEVEL: "warn",
};

export type E2EUser = "alice" | "bob" | "carol";

export function authFile(user: E2EUser): string {
  return path.join(E2E_DIR, ".auth", `${user}.json`);
}

export async function keycloakLogin(page: Page, user: E2EUser): Promise<void> {
  await expect(page).toHaveURL(/localhost:8180\/realms\/geogallery\//);
  await page.locator("#username").fill(user);
  await page.locator("#password").fill(user);
  await page.locator("#kc-login").click();
}
