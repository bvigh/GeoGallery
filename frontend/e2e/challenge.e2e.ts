import { readFileSync } from "node:fs";
import path from "node:path";
import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { E2E_DIR, authFile } from "./support";

const FIXTURE = readFileSync(path.join(E2E_DIR, "fixtures", "gps.jpg"));
const ROUNDS = 5;

// A copy of gps.jpg that the library takes as another file: bytes after the JPEG's end change
// its size, and with it its hash, while its EXIF position stays.
function variant(n: number): Buffer {
  return Buffer.concat([FIXTURE, Buffer.alloc(n + 1, n)]);
}

async function mediaIds(request: APIRequestContext, prefix: string): Promise<string[]> {
  const res = await request.get("/api/media?owner=mine&limit=5000");
  expect(res.status()).toBe(200);
  const items: { id: string; filename: string }[] = await res.json();
  return items.filter((item) => item.filename.startsWith(prefix)).map((item) => item.id);
}

type Point = { x: number; y: number };

// Where bob clicks the guess map: its lower left, clear of the zoom buttons, the corner buttons and
// the credit. Alice clicks its middle, so the two guesses never land on one spot and their pins
// do not cover each other.
const LOWER_LEFT: Point = { x: 50, y: 150 };

// Guesses the round on screen by clicking the guess map (its middle, or `at`), then moves on with
// the button `next` names.
async function playRound(page: Page, number: number, next: string, at?: Point) {
  const view = page.getByRole("dialog");
  await expect(view.getByText(`Round ${number} of ${ROUNDS}`)).toBeVisible();
  const submit = view.getByRole("button", { name: "Submit" });
  await expect(submit).toBeDisabled();
  await view.locator(".leaflet-container").click({ position: at });
  await submit.click();
  await expect(view.getByRole("region", { name: "Round result" })).toBeVisible();
  await view.getByRole("button", { name: next }).click();
}

test("alice makes a challenge for herself and bob, both play it once and compare", async ({ browser }) => {
  const stamp = Date.now();
  const prefix = `challenge-e2e-${stamp}-`;
  const name = `Challenge e2e ${stamp}`;
  const aliceContext = await browser.newContext({ storageState: authFile("alice") });
  const bobContext = await browser.newContext({ storageState: authFile("bob") });
  const alice = await aliceContext.newPage();
  const bob = await bobContext.newPage();
  let albumId: string | null = null;
  let aloneId: string | null = null;
  let challengeId: string | null = null;
  try {
    // Setup through the API: five located photos of alice's, shared with bob through an album.
    // A challenge draws only from what every player can already see.
    for (let n = 0; n < ROUNDS; n++) {
      const res = await alice.request.post("/api/media/upload", {
        multipart: { files: { name: `${prefix}${n}.jpg`, mimeType: "image/jpeg", buffer: variant(n) } },
      });
      expect(res.status()).toBe(200);
    }
    const ids = await mediaIds(alice.request, prefix);
    expect(ids).toHaveLength(ROUNDS);
    const album = await alice.request.post("/api/albums", { data: { name: `Challenge album ${stamp}` } });
    expect(album.status()).toBe(201);
    albumId = (await album.json()).id;
    const bobId: string = (await (await bob.request.get("/api/auth/me")).json()).id;
    expect((await alice.request.post(`/api/albums/${albumId}/members`, { data: { user_id: bobId } })).status()).toBe(204);
    const added = await alice.request.post(`/api/albums/${albumId}/media`, { data: { media_ids: ids } });
    expect(await added.json()).toEqual({ added: ROUNDS });
    // And an album bob is not in.
    const alone = await alice.request.post("/api/albums", { data: { name: `Alone album ${stamp}` } });
    expect(alone.status()).toBe(201);
    aloneId = (await alone.json()).id;

    // Alice makes the challenge on the Game page: the players, then what the items are drawn from
    // (the album, through the dialog's own filters), then a name. It opens at its own URL. The
    // dialog offers only the albums every player is in: ticking bob takes her other album off the
    // list, and unticks it.
    await alice.goto("/game");
    await alice.getByRole("button", { name: "New challenge…" }).click();
    const dialog = alice.getByRole("dialog", { name: "New challenge" });
    const items = dialog.getByRole("group", { name: "Items" });
    const aloneBox = items.getByRole("checkbox", { name: `Alone album ${stamp}` });
    await aloneBox.check();
    await dialog.getByRole("checkbox", { name: /Bob Test/ }).check();
    await expect(aloneBox).toHaveCount(0);
    await expect(dialog.getByRole("status")).toHaveText(`Unticked “Alone album ${stamp}”: not every player is in it.`);
    await items.getByRole("checkbox", { name: `Challenge album ${stamp}` }).check();
    await dialog.getByRole("textbox", { name: "Name (optional)" }).fill(name);
    const created = alice.waitForResponse(
      (r) => r.request().method() === "POST" && new URL(r.url()).pathname === "/api/challenges",
    );
    await dialog.getByRole("button", { name: "Create challenge" }).click();
    expect((await created).status()).toBe(201);
    // The filters chosen in the dialog went with the request, without the album it unticked.
    expect(new URL((await created).url()).searchParams.getAll("album_id")).toEqual([albumId]);
    challengeId = (await (await created).json()).id;

    const aliceView = alice.getByRole("dialog", { name });
    await expect(aliceView.getByText("Your challenge · 2 players")).toBeVisible();
    await expect(alice).toHaveURL(new RegExp(`/game/${challengeId}$`));
    const link = await aliceView.getByRole("textbox", { name: "Link to this challenge" }).inputValue();
    expect(new URL(link).pathname).toBe(`/game/${challengeId}`);

    await aliceView.getByRole("button", { name: `Play with round 1 of ${ROUNDS}` }).click();
    for (let round = 1; round < ROUNDS; round++) await playRound(alice, round, "Next round");
    await playRound(alice, ROUNDS, "See results");
    await expect(aliceView.getByText("You have played every round. Here is how everyone did.")).toBeVisible();
    const aliceScores = aliceView.getByRole("table", { name: "High scores" });
    await expect(aliceScores.getByRole("row").nth(1)).toContainText(`Alice Test (you)${ROUNDS} / ${ROUNDS}`);
    await expect(aliceScores.getByRole("row").nth(2)).toContainText(`Bob Test0 / ${ROUNDS}`);
    // Her own pin with her initials, and the actual location.
    await expect(aliceView.locator(".leaflet-marker-icon")).toHaveCount(2);
    await expect(aliceView.locator(".leaflet-marker-icon", { hasText: "AT" })).toHaveCount(1);

    // Bob opens the link, sees no answer before playing, and plays his five rounds.
    await bob.goto(link);
    const bobView = bob.getByRole("dialog", { name });
    await expect(bobView.getByText("A challenge by Alice Test · 2 players")).toBeVisible();
    await expect(bobView.locator(".leaflet-container")).toHaveCount(0);
    await bobView.getByRole("button", { name: `Play with round 1 of ${ROUNDS}` }).click();
    await playRound(bob, 1, "Next round", LOWER_LEFT);

    // A reload comes back to the challenge, on the round he has not played yet.
    await bob.reload();
    await bobView.getByRole("button", { name: `Continue with round 2 of ${ROUNDS}` }).click();
    for (let round = 2; round < ROUNDS; round++) await playRound(bob, round, "Next round", LOWER_LEFT);
    await playRound(bob, ROUNDS, "See results", LOWER_LEFT);

    // One attempt: a second guess on a round is refused.
    const again = await bob.request.post(`/api/challenges/${challengeId}/guess`, {
      data: { position: 1, guess_lat: 0, guess_lng: 0, guess_date: "" },
    });
    expect(again.status()).toBe(409);

    // Everyone's guesses: a pin with initials per player, and how far off behind a click.
    await expect(bobView.getByRole("table", { name: "High scores" })).toContainText("Bob Test (you)");
    await expect(bobView.locator(".leaflet-marker-icon")).toHaveCount(3);
    const alicePin = bobView.locator(".leaflet-marker-icon", { hasText: "AT" });
    await expect(bobView.locator(".leaflet-marker-icon", { hasText: "BT" })).toHaveCount(1);
    await alicePin.click();
    await expect(bobView.locator(".leaflet-popup")).toContainText("Alice Test");
    await expect(bobView.locator(".leaflet-popup")).toContainText(/km away · [\d,]+ pts/);
    await expect(bobView.getByRole("list", { name: "Guesses of round 1" }).getByRole("listitem")).toHaveCount(2);

    // The round's photo full size, over the results: the photo alone, loaded from its URL. Escape
    // closes it and leaves the challenge open, and so does back.
    const thumbnail = bobView.getByRole("button", { name: "Open round 1's photo full size" });
    await thumbnail.click();
    const fullSize = bob.getByRole("dialog", { name: "Round 1's photo" });
    const photo = fullSize.getByRole("img", { name: "Round 1's photo" });
    await expect(photo).toBeVisible();
    await expect.poll(() => photo.evaluate((img: HTMLImageElement) => (img.complete ? img.naturalWidth : 0))).toBeGreaterThan(0);
    await expect(fullSize.locator(".leaflet-container, .leaflet-marker-icon")).toHaveCount(0);
    await bob.keyboard.press("Escape");
    await expect(fullSize).toHaveCount(0);
    await expect(bobView).toBeVisible();
    await expect(thumbnail).toBeFocused();
    await thumbnail.click();
    await expect(photo).toBeVisible();
    await bob.goBack();
    await expect(fullSize).toHaveCount(0);
    await expect(bobView).toBeVisible();
    await expect(bob).toHaveURL(new RegExp(`/game/${challengeId}$`));

    // The map has a size of its own, on a desktop and on a phone: Leaflet's container takes its
    // height from its parent, so a parent without one would leave it 0 px tall.
    const resultsMap = bobView.locator(".leaflet-container");
    expect((await resultsMap.boundingBox())!.height).toBeGreaterThan(300);
    await bob.setViewportSize({ width: 390, height: 780 });
    await expect.poll(async () => (await resultsMap.boundingBox())!.height).toBeGreaterThan(250);
    expect((await resultsMap.boundingBox())!.width).toBeGreaterThan(300);
    await bob.setViewportSize({ width: 1280, height: 720 });

    // Back closes the challenge before it leaves the Game tab; the list says where bob stands.
    await bob.goBack();
    await expect(bobView).toHaveCount(0);
    await expect(bob).toHaveURL(/\/game$/);
    const card = bob.getByRole("list", { name: "Challenges" }).getByRole("listitem").filter({ hasText: name });
    await expect(card).toContainText("by Alice Test · 2 players");
    await expect(card).toContainText(/Finished · [\d,]+ pts/);
    await expect(card.getByRole("button", { name: `Results: ${name}` })).toBeVisible();

    // Alice deletes it; the link then tells bob it is gone.
    await alice.keyboard.press("Escape");
    await expect(aliceView).toHaveCount(0);
    alice.once("dialog", (d) => d.accept());
    await alice.getByRole("button", { name: `Delete ${name}` }).click();
    await expect(alice.getByRole("button", { name, exact: true })).toHaveCount(0);
    challengeId = null;
    await bob.goto(link);
    await expect(
      bob.getByText("That challenge is not available: it was deleted, or you are not one of its players."),
    ).toBeVisible();
    await expect(bob).toHaveURL(/\/game$/);
  } finally {
    // Teardown through the API, also for a test that failed half-way.
    if (challengeId) await alice.request.delete(`/api/challenges/${challengeId}`).catch(() => {});
    if (albumId) await alice.request.delete(`/api/albums/${albumId}`).catch(() => {});
    if (aloneId) await alice.request.delete(`/api/albums/${aloneId}`).catch(() => {});
    for (const id of await mediaIds(alice.request, prefix).catch(() => [])) {
      await alice.request.delete(`/api/media/${id}`).catch(() => {});
    }
    await aliceContext.close();
    await bobContext.close();
  }
});
