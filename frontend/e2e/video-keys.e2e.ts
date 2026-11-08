import path from "node:path";
import { test, expect, type Locator, type Page } from "@playwright/test";
import { E2E_DIR, authFile } from "./support";

test.use({ storageState: authFile("bob") });

// The browser's native controls exist only here: jsdom has none, and Chromium hides key events
// inside them from the page. A served round needs no upload, so this spec depends on no other.
const CLIP = path.join(E2E_DIR, "fixtures", "clip.webm");

async function startVideoRound(page: Page): Promise<Locator> {
  await page.route("**/api/game/round*", (route) =>
    route.fulfill({
      json: { id: "e2e-video-keys", media_type: "video", url: "/e2e-clip.webm", max_dist_km: 100 },
    })
  );
  await page.route("**/e2e-clip.webm", (route) => route.fulfill({ path: CLIP, contentType: "video/webm" }));

  await page.goto("/");
  await page.getByRole("button", { name: "Game", exact: true }).click();
  await page.getByRole("button", { name: "Start Round" }).click();

  const video = page.locator("video");
  await expect.poll(() => video.evaluate((v: HTMLVideoElement) => v.readyState)).toBeGreaterThanOrEqual(2);
  // Every play and pause, so a second toggle cannot hide behind the first.
  await video.evaluate((v: HTMLVideoElement) => {
    v.dataset.toggles = "0";
    const count = () => {
      v.dataset.toggles = String(Number(v.dataset.toggles) + 1);
    };
    v.addEventListener("play", count);
    v.addEventListener("pause", count);
  });
  return video;
}

const paused = (video: Locator) => video.evaluate((v: HTMLVideoElement) => v.paused);
const muted = (video: Locator) => video.evaluate((v: HTMLVideoElement) => v.muted);

// Runs an action that must toggle playback exactly once, ending in the given state.
async function expectOneToggle(page: Page, video: Locator, nowPaused: boolean, action: () => Promise<unknown>) {
  await video.evaluate((v: HTMLVideoElement) => {
    v.dataset.toggles = "0";
  });
  await action();
  await expect.poll(() => paused(video)).toBe(nowPaused);
  // The event of a second toggle comes a moment after the first.
  await page.waitForTimeout(300);
  await expect(video).toHaveAttribute("data-toggles", "1");
  expect(await paused(video)).toBe(nowPaused);
}

// Chromium's play button, at the left of the control bar. Hovering first brings the bar back
// after it has faded out over a playing video.
async function clickNativePlay(page: Page, video: Locator) {
  const box = (await video.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.waitForTimeout(200);
  await page.mouse.click(box.x + 24, box.y + box.height - 48);
}

test("Space toggles a video once after a click on its controls or picture, and M mutes it", async ({ page }) => {
  const video = await startVideoRound(page);

  await expectOneToggle(page, video, false, () => clickNativePlay(page, video));
  await expectOneToggle(page, video, true, () => page.keyboard.press("Space"));

  await expectOneToggle(page, video, false, () => clickNativePlay(page, video));
  await expectOneToggle(page, video, true, () => clickNativePlay(page, video));
  await expectOneToggle(page, video, false, () => page.keyboard.press("Space"));

  // M right after a click on the controls, where Chromium would keep the key to itself.
  await expectOneToggle(page, video, true, () => clickNativePlay(page, video));
  await page.keyboard.press("m");
  expect(await muted(video)).toBe(true);
  await page.keyboard.press("m");
  expect(await muted(video)).toBe(false);

  // A click on the picture leaves focus on the video element itself.
  const box = (await video.boundingBox())!;
  await expectOneToggle(page, video, false, () =>
    page.mouse.click(box.x + box.width / 2, box.y + box.height / 2)
  );
  await expectOneToggle(page, video, true, () => page.keyboard.press("Space"));

  // Focus on the element with nothing changed yet (Tab, or a click on an empty part of the bar):
  // here Chromium's controls answer Space as well, unless the page stops the key first.
  await video.focus();
  await expectOneToggle(page, video, false, () => page.keyboard.press("Space"));
});
