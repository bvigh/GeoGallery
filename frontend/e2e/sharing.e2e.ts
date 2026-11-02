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
    // album upload below takes the per-owner dedup path.
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
    await aliceMembers.getByLabel("User to add").selectOption({ label: "Bob Test" });
    await aliceMembers.getByRole("button", { name: "Add", exact: true }).click();
    await expect(aliceMembers.getByRole("list", { name: "Members" })).toContainText("Bob Test");
    await alice.keyboard.press("Escape");
    await expect(aliceMembers).toHaveCount(0);

    await alice.getByRole("button", { name: "Upload", exact: true }).click();
    await alice.getByLabel("Add to album").selectOption({ label: albumName });
    await alice.locator('input[type="file"][accept^="image/"]').setInputFiles(FIXTURE);
    await alice.getByRole("main").getByRole("button", { name: "Upload", exact: true }).click();
    await expect(alice.getByText("Uploaded 0 files, skipped 1, failed 0")).toBeVisible();
    await expect(alice.getByText(`Added 1 file to "${albumName}"`)).toBeVisible();

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
    const bobCard = bob.getByRole("article", { name: albumName });
    await expect(bobCard).toContainText("Owner: Alice Test");
    await expect(bobCard).toContainText("2 members · 1 item");

    await alice.getByRole("button", { name: `Members of ${albumName}` }).click();
    alice.once("dialog", (d) => d.accept());
    await aliceMembers.getByRole("button", { name: "Remove Bob Test" }).click();
    await expect(aliceMembers.getByRole("list", { name: "Members" })).not.toContainText("Bob Test");
    await alice.keyboard.press("Escape");
    await expect(aliceMembers).toHaveCount(0);

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
