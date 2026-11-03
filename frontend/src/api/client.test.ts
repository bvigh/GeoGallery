import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";

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

  it("revokeToken() sends DELETE and rejects on failure", async () => {
    fetchMock.mockResolvedValueOnce({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });
    await expect(api.revokeToken("t1")).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith("/api/tokens/t1", { method: "DELETE" });

    fetchMock.mockResolvedValueOnce(response(404));
    await expect(api.revokeToken("t2")).rejects.toMatchObject({ status: 404 });
  });
});
