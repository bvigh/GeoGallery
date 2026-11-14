import { readFileSync } from "node:fs";
import path from "node:path";
import { test, expect, type Page } from "@playwright/test";
import { E2E_DIR, authFile } from "./support";

const FIXTURE = path.join(E2E_DIR, "fixtures", "gps.jpg");

// Mounting a tab fetches its data. Wait for the response, or an empty first render passes for "nothing there".
function nextGet(page: Page, pathname: string) {
  return page.waitForResponse((r) => r.request().method() === "GET" && new URL(r.url()).pathname === pathname);
}

test("alice shares an upload with bob through an album, then removes him", async ({ browser }) => {
  const albumName = `Shared e2e ${Date.now()}`;
  const aliceContext = await browser.newContext({ storageState: authFile("alice") });
  const bobContext = await browser.newContext({ storageState: authFile("bob") });
  const alice = await aliceContext.newPage();
  const bob = await bobContext.newPage();
  let albumId: string | null = null;
  try {
    // Setup: gps.jpg is already in alice's library (the upload spec puts it there too), so the
    // Upload page below finds it by its hash, never sends it, and adds it to the album itself.
    const seeded = await alice.request.post("/api/media/upload", {
      multipart: { files: { name: "gps.jpg", mimeType: "image/jpeg", buffer: readFileSync(FIXTURE) } },
    });
    expect(seeded.status()).toBe(200);

    await alice.goto("/");
    await alice.getByRole("button", { name: "Albums", exact: true }).click();
    await alice.getByRole("button", { name: "New album" }).click();
    const newAlbum = alice.getByRole("dialog");
    await newAlbum.getByLabel("Name").fill(albumName);
    const created = alice.waitForResponse(
      (r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/albums",
    );
    await newAlbum.getByRole("button", { name: "Create album" }).click();
    expect((await created).status()).toBe(201);
    albumId = (await (await created).json()).id;
    const aliceCard = alice.getByRole("article", { name: albumName });
    await expect(aliceCard).toContainText("1 member · 0 items");

    await alice.getByRole("button", { name: `Members of ${albumName}` }).click();
    const aliceMembers = alice.getByRole("dialog");
    await aliceMembers.getByRole("button", { name: "Add Bob Test", exact: true }).click();
    await expect(aliceMembers.getByRole("list", { name: "Members" })).toContainText("Bob Test");
    await alice.keyboard.press("Escape");
    await expect(aliceMembers).toHaveCount(0);

    await alice.getByRole("button", { name: "Upload", exact: true }).click();
    await alice.getByRole("button", { name: "Add to album: None" }).click();
    const picker = alice.getByRole("dialog");
    await picker.getByRole("radio", { name: albumName, exact: true }).check();
    await picker.getByRole("button", { name: "Choose" }).click();
    await expect(picker).toHaveCount(0);
    await expect(alice.getByRole("button", { name: `Add to album: ${albumName}` })).toBeVisible();
    await alice.locator('input[type="file"][accept^="image/"]').setInputFiles(FIXTURE);
    const uploadRequests: string[] = [];
    alice.on("request", (r) => {
      if (new URL(r.url()).pathname === "/api/media/upload") uploadRequests.push(r.url());
    });
    await alice.getByRole("main").getByRole("button", { name: "Upload", exact: true }).click();
    await expect(alice.getByText("Uploaded 0 files, skipped 1, failed 0")).toBeVisible();
    await expect(alice.getByText(`Added 1 file to "${albumName}"`)).toBeVisible();
    expect(uploadRequests, "a file already in the library is never sent").toEqual([]);

    await alice.getByRole("button", { name: "Albums", exact: true }).click();
    await expect(aliceCard).toContainText("2 members · 1 item");

    // Bob's own library is empty, so the one marker is alice's photo.
    await bob.goto("/");
    const marker = bob.locator(".leaflet-marker-pane .leaflet-marker-icon");
    await expect(marker).toHaveCount(1);
    await marker.click();
    await expect(bob.getByText("Shared by Alice Test")).toBeVisible();
    await expect(bob.getByRole("button", { name: "Delete media" })).toHaveCount(0);
    await expect(bob.getByRole("button", { name: "Edit GPS location" })).toHaveCount(0);

    // Trips stay personal: alice's trip for the photo is not bob's.
    const bobTrips = nextGet(bob, "/api/trips");
    await bob.getByRole("button", { name: "Trips", exact: true }).click();
    expect(await (await bobTrips).json()).toEqual([]);
    await expect(bob.getByText("No trips yet. Upload some media to get started.")).toBeVisible();

    await bob.getByRole("button", { name: "Albums", exact: true }).click();
    await expect(bob).toHaveURL(/\/albums$/);
    const bobCard = bob.getByRole("article", { name: albumName });
    await expect(bobCard).toContainText("Owner: Alice Test");
    await expect(bobCard).toContainText("2 members · 1 item");

    // The open gallery and its item are in the URL: a reload opens them again, back then closes
    // the gallery before it leaves the tab, and forward opens it again on the same item.
    await bobCard.getByRole("button", { name: albumName, exact: true }).click();
    await expect(bob.getByText("1 / 1")).toBeVisible();
    await expect(bob).toHaveURL(new RegExp(`/albums/${albumId}\\?item=[0-9a-f-]{36}$`));
    const galleryUrl = bob.url();
    await bob.reload();
    await expect(bob.getByText("1 / 1")).toBeVisible();
    // The gallery's title, once the album list says whose album it is.
    await expect(bob.getByText("by Alice Test", { exact: true })).toBeVisible();
    await expect(bob).toHaveURL(galleryUrl);
    await bob.goBack();
    await expect(bob.getByRole("button", { name: "Close gallery" })).toHaveCount(0);
    await expect(bobCard).toBeVisible();
    await expect(bob).toHaveURL(/\/albums$/);
    // On the gallery's own entry: the forward is the only traversal, so the URL never flashes back
    // to the album list's.
    await bob.evaluate(() => {
      const counted = window as unknown as { e2ePops: number };
      counted.e2ePops = 0;
      window.addEventListener("popstate", () => counted.e2ePops++);
    });
    await bob.goForward();
    await expect(bob.getByText("1 / 1")).toBeVisible();
    await expect(bob).toHaveURL(galleryUrl);
    expect(await bob.evaluate(() => (window as unknown as { e2ePops: number }).e2ePops)).toBe(1);
    await bob.goBack();
    await expect(bob.getByRole("button", { name: "Close gallery" })).toHaveCount(0);
    await expect(bob).toHaveURL(/\/albums$/);

    await alice.getByRole("button", { name: `Members of ${albumName}` }).click();
    alice.once("dialog", (d) => d.accept());
    await aliceMembers.getByRole("button", { name: "Remove Bob Test" }).click();
    await expect(aliceMembers.getByRole("list", { name: "Members" })).not.toContainText("Bob Test");
    await alice.keyboard.press("Escape");
    await expect(aliceMembers).toHaveCount(0);

    // The gallery's link no longer opens the album for bob.
    await bob.goto(galleryUrl);
    await expect(bob.getByText("That album is not available: it was deleted, or you are not a member.")).toBeVisible();
    await expect(bob.getByRole("button", { name: "Close gallery" })).toHaveCount(0);
    await expect(bob).toHaveURL(/\/albums$/);

    const bobMedia = nextGet(bob, "/api/media");
    await bob.getByRole("button", { name: "Map", exact: true }).click();
    expect(await (await bobMedia).json()).toEqual([]);
    await expect(bob.locator(".leaflet-container")).toBeVisible();
    await expect(marker).toHaveCount(0);

    alice.once("dialog", (d) => d.accept());
    await alice.getByRole("button", { name: `Delete ${albumName}` }).click();
    await expect(aliceCard).toHaveCount(0);
    albumId = null;
  } finally {
    // Teardown when the test failed before deleting the album through the UI.
    if (albumId) await alice.request.delete(`/api/albums/${albumId}`).catch(() => {});
    await aliceContext.close();
    await bobContext.close();
  }
});
