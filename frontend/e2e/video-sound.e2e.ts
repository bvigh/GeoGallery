import path from "node:path";
import { test, expect, type Locator, type Page } from "@playwright/test";
import { E2E_DIR, authFile } from "./support";

// Alice plays no video in any spec running beside this one, so her setting starts no other spec's
// video muted. A served round needs no upload.
test.use({ storageState: authFile("alice") });

const CLIP = path.join(E2E_DIR, "fixtures", "clip.webm");

async function serveVideoRounds(page: Page) {
  await page.route("**/api/game/round*", (route) =>
    route.fulfill({
      json: { id: "e2e-video-sound", media_type: "video", url: "/e2e-sound-clip.webm", max_dist_km: 100 },
    })
  );
  await page.route("**/e2e-sound-clip.webm", (route) => route.fulfill({ path: CLIP, contentType: "video/webm" }));
}

// A Game round of the served clip, from the Game page's idle screen.
async function startVideoRound(page: Page): Promise<Locator> {
  await page.getByRole("button", { name: "Game", exact: true }).click();
  await page.getByRole("button", { name: "Start Round" }).click();
  const video = page.locator("video");
  await expect(video).toHaveAttribute("src", "/e2e-sound-clip.webm");
  return video;
}

const muted = (video: Locator) => video.evaluate((v: HTMLVideoElement) => v.muted);

async function chooseInSettings(page: Page, choice: "Muted" | "With sound") {
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  // The page has other sections with a Save button of their own.
  const section = page.getByRole("region", { name: "Video sound" });
  await section.getByRole("radio", { name: new RegExp(`^${choice}`) }).check();
  await section.getByRole("button", { name: "Save", exact: true }).click();
  await expect(section.getByRole("status")).toHaveText("Saved.");
}

// Reloads on the current page and waits for the setting the app loads as it starts.
async function reload(page: Page) {
  const settings = page.waitForResponse((r) => r.url().endsWith("/api/me/settings") && r.ok());
  await page.reload();
  await settings;
}

test("videos start as the Settings page says, at once and after a reload, and M still toggles", async ({ page }) => {
  await serveVideoRounds(page);
  try {
    await page.goto("/settings");
    await expect(
      page.getByRole("region", { name: "Video sound" }).getByRole("radio", { name: "With sound (default)" })
    ).toBeChecked();

    await chooseInSettings(page, "Muted");
    let video = await startVideoRound(page);
    expect(await muted(video)).toBe(true);
    await page.keyboard.press("m");
    expect(await muted(video)).toBe(false);

    await reload(page);
    video = await startVideoRound(page);
    expect(await muted(video)).toBe(true);

    // Chosen, not the default: the app starts videos muted until it has the setting, so only a
    // video with sound after a reload shows that the setting was loaded.
    await chooseInSettings(page, "With sound");
    video = await startVideoRound(page);
    expect(await muted(video)).toBe(false);
    await reload(page);
    video = await startVideoRound(page);
    expect(await muted(video)).toBe(false);
  } finally {
    // Back to the default, whatever happened above.
    const reset = await page.request.patch("/api/me/settings", { data: { videos_start_muted: null } });
    expect(reset.status()).toBe(200);
  }
});
