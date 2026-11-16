import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";
import type { MediaItem } from "@/types";

function response(status: number, body: unknown = {}) {
  return { status, ok: status >= 200 && status < 300, json: async () => body };
}

describe("api client", () => {
  let assign: ReturnType<typeof vi.fn>;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    assign = vi.fn();
    fetchMock = vi.fn();
    vi.stubGlobal("location", { pathname: "/trips", search: "?x=1", origin: "http://localhost", assign });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("redirects to login with return_to on 401", async () => {
    fetchMock.mockResolvedValue(response(401));
    await expect(api.get("/media")).rejects.toBeInstanceOf(ApiError);
    expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
  });

  it("redirects on 401 from helpers that ignore errors", async () => {
    fetchMock.mockResolvedValue(response(401));
    await api.updateLocation("m1", 1, 2);
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it("does not redirect on other errors", async () => {
    fetchMock.mockResolvedValue(response(500));
    await expect(api.get("/media")).rejects.toMatchObject({ status: 500, message: "GET /media failed: 500" });
    expect(assign).not.toHaveBeenCalled();
  });

  it("me() returns null on 401 without redirecting", async () => {
    fetchMock.mockResolvedValue(response(401));
    await expect(api.me()).resolves.toBeNull();
    expect(assign).not.toHaveBeenCalled();
  });

  it("me() returns the current user", async () => {
    const user = { id: "u1", email: "a@example.test", display_name: "Alice", role: "user" };
    fetchMock.mockResolvedValue(response(200, user));
    await expect(api.me()).resolves.toEqual(user);
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/me", {});
  });

  it("logout() posts and follows redirect_url", async () => {
    fetchMock.mockResolvedValue(response(200, { redirect_url: "https://idp.example/logout?x=1" }));
    await api.logout();
    expect(fetchMock).toHaveBeenCalledWith("/api/auth/logout", { method: "POST" });
    expect(assign).toHaveBeenCalledWith("https://idp.example/logout?x=1");
  });

  it("createToken() sends name and expires_in_days", async () => {
    fetchMock.mockResolvedValue(response(201, { id: "t1", token: "gg_pat_x" }));
    await api.createToken("cron", 30);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/tokens");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ name: "cron", expires_in_days: 30 });
  });

  it("adminSetDisabled() accepts a 204 without a body", async () => {
    fetchMock.mockResolvedValue({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });
    await expect(api.adminSetDisabled("u2", true)).resolves.toBeUndefined();
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/admin/users/u2");
    expect(init.method).toBe("PATCH");
    expect(JSON.parse(init.body)).toEqual({ disabled: true });
  });

  it("adminCreateInvite() returns the invite url", async () => {
    fetchMock.mockResolvedValue(response(201, { url: "http://localhost:5173/api/auth/login?invite=abc" }));
    await expect(api.adminCreateInvite("user")).resolves.toBe("http://localhost:5173/api/auth/login?invite=abc");
  });

  it("uploadGpsTracks() posts every file as a multipart files part", async () => {
    const result = { inserted: 2, skipped: 0, empty: [], failed: [] };
    fetchMock.mockResolvedValue(response(200, result));
    const files = [new File(["<gpx/>"], "walk.gpx"), new File(["$GPRMC"], "drive.nmea")];
    await expect(api.uploadGpsTracks(files)).resolves.toEqual(result);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/gps-tracks/upload");
    expect(init.method).toBe("POST");
    expect((init.body as FormData).getAll("files").map((f) => (f as File).name)).toEqual(["walk.gpx", "drive.nmea"]);
  });

  it("uploadGpsTracks() throws an ApiError with the status", async () => {
    fetchMock.mockResolvedValue(response(413, {}));
    await expect(api.uploadGpsTracks([new File(["x"], "big.json")])).rejects.toMatchObject({ status: 413 });
  });

  it("upload() posts the form data and returns uploaded names", async () => {
    fetchMock.mockResolvedValue(response(200, { uploaded: ["a.jpg"] }));
    const form = new FormData();
    await expect(api.upload(form)).resolves.toEqual({ uploaded: ["a.jpg"] });
    expect(fetchMock).toHaveBeenCalledWith("/api/media/upload", { method: "POST", body: form });
  });

  it("listTokens() returns the token list", async () => {
    const tokens = [{ id: "t1", name: "cron" }];
    fetchMock.mockResolvedValue(response(200, tokens));
    await expect(api.listTokens()).resolves.toEqual(tokens);
    expect(fetchMock.mock.calls[0][0]).toBe("/api/tokens");
  });

  it("upload() sends album_id as a query param when given", async () => {
    fetchMock.mockResolvedValue(response(200, { uploaded: ["a.jpg"], skipped: ["b.jpg"], album_added: 2 }));
    const form = new FormData();
    await expect(api.upload(form, "a1")).resolves.toEqual({ uploaded: ["a.jpg"], skipped: ["b.jpg"], album_added: 2 });
    expect(fetchMock).toHaveBeenCalledWith("/api/media/upload?album_id=a1", { method: "POST", body: form });
  });

  it("upload() sends the upload's camera zone as a query param when given", async () => {
    fetchMock.mockResolvedValue(response(200, { uploaded: ["a.jpg"] }));
    const form = new FormData();
    await api.upload(form, undefined, "America/New_York");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/media/upload?camera_timezone=America%2FNew_York", { method: "POST", body: form });
    await api.upload(form, "a1", "Asia/Tokyo");
    expect(fetchMock).toHaveBeenLastCalledWith("/api/media/upload?album_id=a1&camera_timezone=Asia%2FTokyo", { method: "POST", body: form });
  });

  it("upload() rejects with the status when the album is not found", async () => {
    fetchMock.mockResolvedValue(response(404));
    await expect(api.upload(new FormData(), "a1")).rejects.toMatchObject({ status: 404 });
  });

  it("knownHashes() posts the hashes and returns the known map", async () => {
    fetchMock.mockResolvedValue(response(200, { known: { h1: "m1" } }));
    await expect(api.knownHashes(["h1", "h2"])).resolves.toEqual({ h1: "m1" });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/media/known-hashes");
    expect(init.method).toBe("POST");
    expect(JSON.parse(init.body)).toEqual({ hashes: ["h1", "h2"] });
  });

  describe("listMedia()", () => {
    // A page of GET /api/media: its items, and the next page's cursor unless it is the last.
    function page(ids: string[], next?: string) {
      return { ...response(200, ids.map((id) => ({ id }))), headers: new Headers(next ? { "X-Next-Cursor": next } : {}) };
    }
    const ids = (items: MediaItem[]) => items.map((m) => m.id);

    it("follows the cursor to the page that names none, in pages of the largest size", async () => {
      fetchMock.mockResolvedValueOnce(page(["m1", "m2"], "c1")).mockResolvedValueOnce(page(["m3"], "c2")).mockResolvedValueOnce(page(["m4"]));
      const onPage = vi.fn();

      const items = await api.listMedia({ album_id: "a1", media_type: ["photo", "video"], owner: "" }, { onPage });

      expect(ids(items)).toEqual(["m1", "m2", "m3", "m4"]);
      expect(onPage.mock.calls.map(([p]) => ids(p))).toEqual([["m1", "m2"], ["m3"], ["m4"]]);
      // The filters stay the same on every page; only the cursor moves.
      expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
        "/api/media?album_id=a1&media_type=photo&media_type=video&limit=5000",
        "/api/media?album_id=a1&media_type=photo&media_type=video&limit=5000&cursor=c1",
        "/api/media?album_id=a1&media_type=photo&media_type=video&limit=5000&cursor=c2",
      ]);
    });

    it("takes a page without a cursor as the last, however full", async () => {
      fetchMock.mockResolvedValueOnce(page(Array.from({ length: 5000 }, (_, i) => `m${i}`)));
      await expect(api.listMedia({})).resolves.toHaveLength(5000);
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    it("sends the cursor as it came", async () => {
      fetchMock.mockResolvedValueOnce(page(["m1"], "MjAyNC0wNS0wMlQxMjowMDowMlog_-")).mockResolvedValueOnce(page(["m2"]));
      await api.listMedia({});
      expect(fetchMock.mock.calls[1][0]).toBe("/api/media?limit=5000&cursor=MjAyNC0wNS0wMlQxMjowMDowMlog_-");
    });

    it("rejects with the status of a page that failed", async () => {
      fetchMock.mockResolvedValueOnce(page(["m1"], "c1")).mockResolvedValueOnce(response(500));
      const onPage = vi.fn();
      await expect(api.listMedia({}, { onPage })).rejects.toMatchObject({ status: 500, message: "GET /media failed: 500" });
      expect(onPage).toHaveBeenCalledTimes(1);
      expect(assign).not.toHaveBeenCalled();
    });

    it("redirects to login on 401", async () => {
      fetchMock.mockResolvedValue(response(401));
      await expect(api.listMedia({})).rejects.toBeInstanceOf(ApiError);
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });

    it("passes the signal to each request and asks for no page after an abort", async () => {
      const list = new AbortController();
      fetchMock.mockResolvedValueOnce(page(["m1"], "c1")).mockResolvedValueOnce(page(["m2"]));
      const onPage = vi.fn(() => list.abort());

      await expect(api.listMedia({}, { onPage, signal: list.signal })).rejects.toMatchObject({ name: "AbortError" });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0][1]).toEqual({ signal: list.signal });
    });

    it("hands over no page that arrives after an abort", async () => {
      const list = new AbortController();
      let answer!: (res: unknown) => void;
      fetchMock.mockReturnValueOnce(new Promise((resolve) => (answer = resolve)));
      const onPage = vi.fn();

      const listed = api.listMedia({}, { onPage, signal: list.signal });
      list.abort();
      answer(page(["m1"], "c1"));

      await expect(listed).rejects.toMatchObject({ name: "AbortError" });
      expect(onPage).not.toHaveBeenCalled();
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
  });

  describe("albums", () => {
    const sampleAlbum = { id: "a1", name: "Iceland", owner_id: "u1", is_owner: true };
    const noBody = () => ({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });

    it.each([
      {
        name: "listAlbums()",
        call: () => api.listAlbums(),
        reply: response(200, [sampleAlbum]),
        url: "/api/albums",
        method: undefined,
        body: undefined,
        want: [sampleAlbum],
      },
      {
        name: "createAlbum()",
        call: () => api.createAlbum("Iceland"),
        reply: response(201, sampleAlbum),
        url: "/api/albums",
        method: "POST",
        body: { name: "Iceland" },
        want: sampleAlbum,
      },
      {
        name: "renameAlbum() accepts a 204",
        call: () => api.renameAlbum("a1", "Iceland 2025"),
        reply: noBody(),
        url: "/api/albums/a1",
        method: "PATCH",
        body: { name: "Iceland 2025" },
        want: undefined,
      },
      {
        name: "deleteAlbum()",
        call: () => api.deleteAlbum("a1"),
        reply: noBody(),
        url: "/api/albums/a1",
        method: "DELETE",
        body: undefined,
        want: undefined,
      },
      {
        name: "listAlbumMembers()",
        call: () => api.listAlbumMembers("a1"),
        reply: response(200, [{ user_id: "u1", is_owner: true }]),
        url: "/api/albums/a1/members",
        method: undefined,
        body: undefined,
        want: [{ user_id: "u1", is_owner: true }],
      },
      {
        name: "addAlbumMember() sends user_id",
        call: () => api.addAlbumMember("a1", "u2"),
        reply: noBody(),
        url: "/api/albums/a1/members",
        method: "POST",
        body: { user_id: "u2" },
        want: undefined,
      },
      {
        name: "removeAlbumMember()",
        call: () => api.removeAlbumMember("a1", "u2"),
        reply: noBody(),
        url: "/api/albums/a1/members/u2",
        method: "DELETE",
        body: undefined,
        want: undefined,
      },
      {
        name: "addToAlbum() with media ids",
        call: () => api.addToAlbum("a1", { mediaIds: ["m1", "m2"] }),
        reply: response(200, { added: 2 }),
        url: "/api/albums/a1/media",
        method: "POST",
        body: { media_ids: ["m1", "m2"] },
        want: { added: 2 },
      },
      {
        name: "addToAlbum() with a trip",
        call: () => api.addToAlbum("a1", { tripId: "t1" }),
        reply: response(200, { added: 7 }),
        url: "/api/albums/a1/media",
        method: "POST",
        body: { trip_id: "t1" },
        want: { added: 7 },
      },
      {
        name: "removeFromAlbum() with media ids",
        call: () => api.removeFromAlbum("a1", { mediaIds: ["m1"] }),
        reply: response(200, { removed: 1 }),
        url: "/api/albums/a1/media/remove",
        method: "POST",
        body: { media_ids: ["m1"] },
        want: { removed: 1 },
      },
      {
        name: "removeFromAlbum() with a trip",
        call: () => api.removeFromAlbum("a1", { tripId: "t1" }),
        reply: response(200, { removed: 3 }),
        url: "/api/albums/a1/media/remove",
        method: "POST",
        body: { trip_id: "t1" },
        want: { removed: 3 },
      },
      {
        name: "mediaAlbums()",
        call: () => api.mediaAlbums("m1"),
        reply: response(200, [{ id: "a1", name: "Iceland" }]),
        url: "/api/media/m1/albums",
        method: undefined,
        body: undefined,
        want: [{ id: "a1", name: "Iceland" }],
      },
      {
        name: "listUsers()",
        call: () => api.listUsers(),
        reply: response(200, [{ id: "u2", display_name: "Bob", email: null }]),
        url: "/api/users",
        method: undefined,
        body: undefined,
        want: [{ id: "u2", display_name: "Bob", email: null }],
      },
    ])("$name", async ({ call, reply, url, method, body, want }) => {
      fetchMock.mockResolvedValue(reply);
      await expect(call()).resolves.toEqual(want);
      const [gotUrl, init] = fetchMock.mock.calls[0];
      expect(gotUrl).toBe(url);
      expect(init.method).toBe(method);
      if (body === undefined) {
        expect(init.body).toBeUndefined();
      } else {
        expect(init.headers).toEqual({ "Content-Type": "application/json" });
        expect(JSON.parse(init.body)).toEqual(body);
      }
    });

    it.each([
      { name: "listAlbums()", call: () => api.listAlbums() },
      { name: "listAlbumMembers()", call: () => api.listAlbumMembers("a1") },
      { name: "mediaAlbums()", call: () => api.mediaAlbums("m1") },
      { name: "listUsers()", call: () => api.listUsers() },
    ])("$name turns a null list into []", async ({ call }) => {
      fetchMock.mockResolvedValue(response(200, null));
      await expect(call()).resolves.toEqual([]);
    });

    it.each([
      { name: "renameAlbum() as a member", call: () => api.renameAlbum("a1", "x"), status: 403, message: "PATCH /albums/a1 failed: 403" },
      { name: "addAlbumMember() for an unknown user", call: () => api.addAlbumMember("a1", "u9"), status: 404, message: "POST /albums/a1/members failed: 404" },
      { name: "removeAlbumMember() for the owner", call: () => api.removeAlbumMember("a1", "u1"), status: 400, message: "DELETE /albums/a1/members/u1 failed: 400" },
      { name: "removeFromAlbum() for a foreign item", call: () => api.removeFromAlbum("a1", { mediaIds: ["m9"] }), status: 403, message: "POST /albums/a1/media/remove failed: 403" },
      { name: "deleteAlbum() for a non-member", call: () => api.deleteAlbum("a9"), status: 404, message: "DELETE /albums/a9 failed: 404" },
    ])("$name rejects with the status", async ({ call, status, message }) => {
      fetchMock.mockResolvedValue(response(status));
      await expect(call()).rejects.toMatchObject({ status, message });
      expect(assign).not.toHaveBeenCalled();
    });

    it("redirects to login on 401", async () => {
      fetchMock.mockResolvedValue(response(401));
      await expect(api.addToAlbum("a1", { tripId: "t1" })).rejects.toBeInstanceOf(ApiError);
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });
  });

  describe("provenance", () => {
    const signal = new AbortController().signal;

    it("mediaProvenance() fetches the item's provenance", async () => {
      const body = { item: { id: "m1" } };
      fetchMock.mockResolvedValue(response(200, body));
      await expect(api.mediaProvenance("m1")).resolves.toEqual(body);
      expect(fetchMock.mock.calls[0][0]).toBe("/api/media/m1/provenance");
    });

    it("mediaProvenanceFile() passes the abort signal to fetch", async () => {
      const body = { basis: "reread_now" };
      fetchMock.mockResolvedValue(response(200, body));
      await expect(api.mediaProvenanceFile("m1", signal)).resolves.toEqual(body);
      expect(fetchMock).toHaveBeenCalledWith("/api/media/m1/provenance/file", { signal });
    });

    it("mediaProvenanceFile() lets an abort reach the caller", async () => {
      const aborted = new DOMException("Aborted", "AbortError");
      fetchMock.mockRejectedValue(aborted);
      await expect(api.mediaProvenanceFile("m1", signal)).rejects.toBe(aborted);
    });

    it("runMatcher() posts without a body and ignores the reply", async () => {
      fetchMock.mockResolvedValue(response(200, { ok: true }));
      await expect(api.runMatcher()).resolves.toBeUndefined();
      expect(fetchMock).toHaveBeenCalledWith("/api/gps-matcher/run", { method: "POST" });
    });

    it.each([
      {
        name: "mediaProvenance() for a foreign item",
        call: () => api.mediaProvenance("m9"),
        status: 404,
        message: "GET /media/m9/provenance failed: 404",
      },
      {
        name: "mediaProvenanceFile() when storage fails",
        call: () => api.mediaProvenanceFile("m1", signal),
        status: 502,
        message: "GET /media/m1/provenance/file failed: 502",
      },
      {
        name: "mediaProvenanceFile() on a timeout",
        call: () => api.mediaProvenanceFile("m1", signal),
        status: 504,
        message: "GET /media/m1/provenance/file failed: 504",
      },
      {
        name: "runMatcher() when the matcher stops",
        call: () => api.runMatcher(),
        status: 500,
        message: "POST /gps-matcher/run failed: 500",
      },
    ])("$name rejects with the status", async ({ call, status, message }) => {
      fetchMock.mockResolvedValue(response(status));
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status, message });
      expect(assign).not.toHaveBeenCalled();
    });

    it.each([
      { name: "mediaProvenance()", call: () => api.mediaProvenance("m1") },
      { name: "mediaProvenanceFile()", call: () => api.mediaProvenanceFile("m1", signal) },
      { name: "runMatcher()", call: () => api.runMatcher() },
    ])("$name redirects to login on 401", async ({ call }) => {
      fetchMock.mockResolvedValue(response(401));
      await expect(call()).rejects.toBeInstanceOf(ApiError);
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });
  });

  describe("checks", () => {
    it.each([
      { name: "checks()", call: (signal?: AbortSignal) => api.checks(signal), url: "/api/checks" },
      {
        name: "checksAgreement() from the first file",
        call: (signal?: AbortSignal) => api.checksAgreement(0, signal),
        url: "/api/checks/agreement?from=0",
      },
      {
        name: "checksAgreement() from where the last request stopped",
        call: (signal?: AbortSignal) => api.checksAgreement(1500, signal),
        url: "/api/checks/agreement?from=1500",
      },
    ])("$name fetches with the signal that cancels it", async ({ call, url }) => {
      const body = { generated_at: "2000-01-01T00:00:00Z" };
      const signal = new AbortController().signal;
      fetchMock.mockResolvedValue(response(200, body));
      await expect(call(signal)).resolves.toEqual(body);
      expect(fetchMock).toHaveBeenCalledWith(url, { signal });
    });

    it("checks() works without a signal", async () => {
      fetchMock.mockResolvedValue(response(200, {}));
      await api.checks();
      expect(fetchMock).toHaveBeenCalledWith("/api/checks", { signal: undefined });
    });

    it.each([
      { name: "checks() on the server's time limit", call: () => api.checks(), status: 504, message: "GET /checks failed: 504" },
      { name: "checks() for a token", call: () => api.checks(), status: 403, message: "GET /checks failed: 403" },
      {
        name: "checksAgreement() when the database fails",
        call: () => api.checksAgreement(0),
        status: 500,
        message: "GET /checks/agreement failed: 500",
      },
      {
        name: "checksAgreement() while the server runs other comparisons",
        call: () => api.checksAgreement(300),
        status: 503,
        message: "GET /checks/agreement failed: 503",
      },
      { name: "checksAgreement() on a bad index", call: () => api.checksAgreement(-1), status: 400, message: "GET /checks/agreement failed: 400" },
    ])("$name rejects with the status", async ({ call, status, message }) => {
      fetchMock.mockResolvedValue(response(status));
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status, message });
      expect(assign).not.toHaveBeenCalled();
    });

    it.each([
      { name: "checks()", call: () => api.checks() },
      { name: "checksAgreement()", call: () => api.checksAgreement(0) },
    ])("$name redirects to login on 401", async ({ call }) => {
      fetchMock.mockResolvedValue(response(401));
      await expect(call()).rejects.toBeInstanceOf(ApiError);
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });

    it("lets an abort reach the caller", async () => {
      const aborted = new DOMException("Aborted", "AbortError");
      fetchMock.mockRejectedValue(aborted);
      await expect(api.checksAgreement(0, new AbortController().signal)).rejects.toBe(aborted);
    });
  });

  describe("camera clock rules", () => {
    const input = {
      make: "Panasonic",
      model: "DC-TZ200",
      utc_offset_min: 120,
      starts_on: "2018-07-01",
      ends_on: null,
      note: "",
    };
    const rule = { ...input, id: "r1", created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z" };
    const camera = { make: "Panasonic", model: "DC-TZ200", items: 595, first_taken_at: null, last_taken_at: null };
    const noBody = () => ({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });

    it.each([
      {
        name: "listCameraClockRules()",
        call: () => api.listCameraClockRules(),
        reply: response(200, [rule]),
        url: "/api/camera-clock-rules",
        method: undefined,
        body: undefined,
        want: [rule],
      },
      {
        name: "createCameraClockRule()",
        call: () => api.createCameraClockRule(input),
        reply: response(201, rule),
        url: "/api/camera-clock-rules",
        method: "POST",
        body: input,
        want: rule,
      },
      {
        name: "updateCameraClockRule() replaces the rule",
        call: () => api.updateCameraClockRule("r1", input),
        reply: response(200, rule),
        url: "/api/camera-clock-rules/r1",
        method: "PUT",
        body: input,
        want: rule,
      },
      {
        name: "deleteCameraClockRule() accepts a 204",
        call: () => api.deleteCameraClockRule("r1"),
        reply: noBody(),
        url: "/api/camera-clock-rules/r1",
        method: "DELETE",
        body: undefined,
        want: undefined,
      },
      {
        name: "listCameras()",
        call: () => api.listCameras(),
        reply: response(200, [camera]),
        url: "/api/cameras",
        method: undefined,
        body: undefined,
        want: [camera],
      },
    ])("$name", async ({ call, reply, url, method, body, want }) => {
      fetchMock.mockResolvedValue(reply);
      await expect(call()).resolves.toEqual(want);
      const [gotUrl, init] = fetchMock.mock.calls[0];
      expect(gotUrl).toBe(url);
      expect(init.method).toBe(method);
      if (body === undefined) {
        expect(init.body).toBeUndefined();
      } else {
        expect(init.headers).toEqual({ "Content-Type": "application/json" });
        expect(JSON.parse(init.body)).toEqual(body);
      }
    });

    it.each([
      { name: "listCameraClockRules()", call: () => api.listCameraClockRules() },
      { name: "listCameras()", call: () => api.listCameras() },
    ])("$name turns a null list into []", async ({ call }) => {
      fetchMock.mockResolvedValue(response(200, null));
      await expect(call()).resolves.toEqual([]);
    });

    it.each([
      { name: "createCameraClockRule()", call: () => api.createCameraClockRule(input), message: "POST /camera-clock-rules failed: 400" },
      { name: "updateCameraClockRule()", call: () => api.updateCameraClockRule("r1", input), message: "PUT /camera-clock-rules/r1 failed: 400" },
    ])("$name carries the reason of a 400", async ({ call, message }) => {
      fetchMock.mockResolvedValue({ ...response(400), text: async () => "utc_offset_min must be a multiple of 15\n" });
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status: 400, message, reason: "utc_offset_min must be a multiple of 15" });
    });

    it("has no reason for a 400 with an empty or unreadable body", async () => {
      fetchMock.mockResolvedValueOnce({ ...response(400), text: async () => "  " });
      await expect(api.createCameraClockRule(input)).rejects.toMatchObject({ status: 400, reason: null });
      fetchMock.mockResolvedValueOnce({ ...response(400), text: async () => { throw new Error("gone"); } });
      await expect(api.createCameraClockRule(input)).rejects.toMatchObject({ status: 400, reason: null });
    });

    it.each([
      { name: "createCameraClockRule()", call: () => api.createCameraClockRule(input), reason: "at most 100 camera clock rules" },
      { name: "updateCameraClockRule()", call: () => api.updateCameraClockRule("r1", input), reason: "overlaps another rule for this camera" },
    ])("$name carries the reason of a 409, which tells an overlap from the cap", async ({ call, reason }) => {
      fetchMock.mockResolvedValue({ ...response(409), text: async () => reason + "\n" });
      await expect(call()).rejects.toMatchObject({ status: 409, reason });
    });

    it.each([
      { name: "a foreign rule", call: () => api.updateCameraClockRule("r9", input), status: 404, message: "PUT /camera-clock-rules/r9 failed: 404" },
      { name: "a delete of a foreign rule", call: () => api.deleteCameraClockRule("r9"), status: 404, message: "DELETE /camera-clock-rules/r9 failed: 404" },
    ])("rejects $name with the status and no reason", async ({ call, status, message }) => {
      const text = vi.fn(async () => "overlaps another rule for this camera");
      fetchMock.mockResolvedValue({ ...response(status), text });
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status, message, reason: null });
      expect(text).not.toHaveBeenCalled();
      expect(assign).not.toHaveBeenCalled();
    });

    it("redirects to login on 401", async () => {
      fetchMock.mockResolvedValue(response(401));
      await expect(api.createCameraClockRule(input)).rejects.toBeInstanceOf(ApiError);
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });
  });

  it("revokeToken() sends DELETE and rejects on failure", async () => {
    fetchMock.mockResolvedValueOnce({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });
    await expect(api.revokeToken("t1")).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith("/api/tokens/t1", { method: "DELETE" });

    fetchMock.mockResolvedValueOnce(response(404));
    await expect(api.revokeToken("t2")).rejects.toMatchObject({ status: 404 });
  });

  describe("settings", () => {
    const settings = { camera_timezone: "Asia/Tokyo", defaults: { camera_timezone: "Europe/Warsaw" } };

    it("getSettings() reads the caller's settings", async () => {
      fetchMock.mockResolvedValue(response(200, settings));
      await expect(api.getSettings()).resolves.toEqual(settings);
      expect(fetchMock.mock.calls[0][0]).toBe("/api/me/settings");
    });

    it("updateSettings() sends a PATCH with the changes only", async () => {
      fetchMock.mockResolvedValue(response(200, { ...settings, camera_timezone: null }));
      await expect(api.updateSettings({ camera_timezone: null })).resolves.toMatchObject({ camera_timezone: null });
      const [url, init] = fetchMock.mock.calls[0];
      expect(url).toBe("/api/me/settings");
      expect(init).toMatchObject({ method: "PATCH", headers: { "Content-Type": "application/json" } });
      expect(JSON.parse(init.body)).toEqual({ camera_timezone: null });
    });

    it("updateSettings() carries the reason of a 400", async () => {
      fetchMock.mockResolvedValue({ ...response(400), text: async () => 'camera_timezone: unknown time zone "Mars/Olympus"\n' });
      await expect(api.updateSettings({ camera_timezone: "Mars/Olympus" })).rejects.toMatchObject({
        status: 400,
        message: "PATCH /me/settings failed: 400",
        reason: 'camera_timezone: unknown time zone "Mars/Olympus"',
      });
    });

    it("updateSettings() has no reason for other failures", async () => {
      const text = vi.fn(async () => "db error");
      fetchMock.mockResolvedValue({ ...response(500), text });
      await expect(api.updateSettings({ camera_timezone: null })).rejects.toMatchObject({ status: 500, reason: null });
      expect(text).not.toHaveBeenCalled();
    });
  });

  describe("capture times", () => {
    const setResult = { taken_at: "2019-03-13T17:28:15Z", trip_id: "t1", followup_failed: false };
    const shiftResult = { shifted: 2, undated: 1, trips: [{ id: "t1", items: 2 }], followup_failed: false };

    it.each([
      {
        name: "setTakenAt() with an IANA zone",
        call: () => api.setTakenAt("m1", "2019-03-13T18:28:15", { zone: "Europe/Warsaw" }),
        reply: response(200, setResult),
        url: "/api/media/m1/taken-at",
        method: "PATCH",
        body: { wall: "2019-03-13T18:28:15", zone: "Europe/Warsaw" },
        want: setResult,
      },
      {
        name: "setTakenAt() with a fixed offset",
        call: () => api.setTakenAt("m1", "2019-03-13T19:28:15", { utcOffsetMin: 120 }),
        reply: response(200, setResult),
        url: "/api/media/m1/taken-at",
        method: "PATCH",
        body: { wall: "2019-03-13T19:28:15", utc_offset_min: 120 },
        want: setResult,
      },
      {
        name: "setTakenAt() with a negative offset",
        call: () => api.setTakenAt("m1", "2019-03-13T13:58:15", { utcOffsetMin: -210 }),
        reply: response(200, setResult),
        url: "/api/media/m1/taken-at",
        method: "PATCH",
        body: { wall: "2019-03-13T13:58:15", utc_offset_min: -210 },
        want: setResult,
      },
      {
        name: "shiftTimes()",
        call: () => api.shiftTimes(["m1", "m2", "m3"], -3600),
        reply: response(200, shiftResult),
        url: "/api/media/shift-time",
        method: "POST",
        body: { media_ids: ["m1", "m2", "m3"], delta_seconds: -3600 },
        want: shiftResult,
      },
    ])("$name", async ({ call, reply, url, method, body, want }) => {
      fetchMock.mockResolvedValue(reply);
      await expect(call()).resolves.toEqual(want);
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const [gotUrl, init] = fetchMock.mock.calls[0];
      expect(gotUrl).toBe(url);
      expect(init.method).toBe(method);
      expect(init.headers).toEqual({ "Content-Type": "application/json" });
      expect(JSON.parse(init.body)).toEqual(body);
    });

    it.each([
      {
        name: "setTakenAt()",
        call: () => api.setTakenAt("m1", "2025-03-30T02:30:00", { zone: "Europe/Warsaw" }),
        message: "PATCH /media/m1/taken-at failed: 400",
        reason: "2025-03-30T02:30:00 does not exist in Europe/Warsaw: the clocks skipped it",
      },
      {
        name: "shiftTimes()",
        call: () => api.shiftTimes(["m1"], 400_000_000_000),
        message: "POST /media/shift-time failed: 400",
        reason: "delta_seconds is too large",
      },
    ])("$name carries the reason of a 400", async ({ call, message, reason }) => {
      fetchMock.mockResolvedValue({ ...response(400), text: async () => reason + "\n" });
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status: 400, message, reason });
    });

    it("has no reason for a 400 with an empty or unreadable body", async () => {
      fetchMock.mockResolvedValueOnce({ ...response(400), text: async () => " \n" });
      await expect(api.shiftTimes(["m1"], 60)).rejects.toMatchObject({ status: 400, reason: null });
      fetchMock.mockResolvedValueOnce({ ...response(400), text: async () => { throw new Error("gone"); } });
      await expect(api.setTakenAt("m1", "2019-03-13T18:28:15", { utcOffsetMin: 0 })).rejects.toMatchObject({
        status: 400,
        reason: null,
      });
    });

    it.each([
      { name: "setTakenAt() on a foreign item", call: () => api.setTakenAt("m9", "2019-03-13T18:28:15", { zone: "UTC" }), status: 404, message: "PATCH /media/m9/taken-at failed: 404" },
      { name: "shiftTimes() with a foreign item", call: () => api.shiftTimes(["m1", "m9"], 60), status: 404, message: "POST /media/shift-time failed: 404" },
      { name: "setTakenAt() on a server error", call: () => api.setTakenAt("m1", "2019-03-13T18:28:15", { zone: "UTC" }), status: 500, message: "PATCH /media/m1/taken-at failed: 500" },
      { name: "shiftTimes() on a body too large", call: () => api.shiftTimes(["m1"], 60), status: 413, message: "POST /media/shift-time failed: 413" },
    ])("rejects $name with the status and no reason", async ({ call, status, message }) => {
      const text = vi.fn(async () => "not for the user");
      fetchMock.mockResolvedValue({ ...response(status), text });
      const error = await call().catch((e: unknown) => e);
      expect(error).toBeInstanceOf(ApiError);
      expect(error).toMatchObject({ status, message, reason: null });
      expect(text).not.toHaveBeenCalled();
      expect(assign).not.toHaveBeenCalled();
    });

    it.each([
      { name: "setTakenAt()", call: () => api.setTakenAt("m1", "2019-03-13T18:28:15", { zone: "UTC" }) },
      { name: "shiftTimes()", call: () => api.shiftTimes(["m1"], 60) },
    ])("$name redirects to login on 401", async ({ call }) => {
      fetchMock.mockResolvedValue(response(401));
      await expect(call()).rejects.toMatchObject({ status: 401 });
      expect(assign).toHaveBeenCalledWith("/api/auth/login?return_to=" + encodeURIComponent("/trips?x=1"));
    });
  });
});
