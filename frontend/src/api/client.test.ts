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

  it("importGpsTracks() posts the file as JSON", async () => {
    fetchMock.mockResolvedValue(response(200, { inserted: 2, skipped: 0, errors: [] }));
    const file = new File(["[]"], "points.json", { type: "application/json" });
    await expect(api.importGpsTracks(file)).resolves.toEqual({ inserted: 2, skipped: 0, errors: [] });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/gps-tracks/import");
    expect(init.body).toBe(file);
    expect(init.headers).toEqual({ "Content-Type": "application/json" });
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

  it("revokeToken() sends DELETE and rejects on failure", async () => {
    fetchMock.mockResolvedValueOnce({ status: 204, ok: true, json: async () => { throw new Error("no body"); } });
    await expect(api.revokeToken("t1")).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith("/api/tokens/t1", { method: "DELETE" });

    fetchMock.mockResolvedValueOnce(response(404));
    await expect(api.revokeToken("t2")).rejects.toMatchObject({ status: 404 });
  });
});
