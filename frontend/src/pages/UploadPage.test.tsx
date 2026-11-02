import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { UploadPage } from "@/pages/UploadPage";
import type { Album } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: { upload: vi.fn(), importGpsTracks: vi.fn(), listAlbums: vi.fn() },
  };
});

const mockApi = api as unknown as {
  upload: ReturnType<typeof vi.fn>;
  importGpsTracks: ReturnType<typeof vi.fn>;
  listAlbums: ReturnType<typeof vi.fn>;
};

function mediaInput(container: HTMLElement): HTMLInputElement {
  return container.querySelector('input[type="file"][multiple]') as HTMLInputElement;
}

function makeAlbum(overrides: Partial<Album>): Album {
  return {
    id: "a1",
    name: "Family",
    owner_id: "u-me",
    owner_name: "Me",
    is_owner: true,
    member_count: 2,
    media_count: 3,
    start_date: null,
    end_date: null,
    created_at: "2026-01-01T00:00:00Z",
    ...overrides,
  };
}

const family = makeAlbum({ id: "a1", name: "Family" });
const iceland = makeAlbum({ id: "a2", name: "Iceland 2025", owner_id: "u-bob", owner_name: "Bob", is_owner: false });

// Each file is over half the 512 MiB batch target, so each gets a request of its own.
function bigVideos(...names: string[]): File[] {
  return names.map((name) => {
    const f = new File(["x"], name, { type: "video/mp4" });
    Object.defineProperty(f, "size", { value: 400 * 1024 ** 2 });
    return f;
  });
}

async function chooseAlbum(name: string) {
  await screen.findByRole("option", { name });
  await userEvent.selectOptions(screen.getByRole("combobox", { name: "Add to album" }), name);
}

describe("UploadPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg", "b.mp4"] });
    mockApi.importGpsTracks.mockResolvedValue({ inserted: 5, skipped: 2, errors: [{ row: 3, reason: "bad lat" }] });
    mockApi.listAlbums.mockResolvedValue([family, iceland]);
  });

  it("uploads files with their mtimes through api.upload", async () => {
    const { container } = render(<UploadPage />);
    const a = new File(["a"], "a.jpg", { type: "image/jpeg", lastModified: 1700000000000 });
    const b = new File(["b"], "b.mp4", { type: "video/mp4", lastModified: 1700000001000 });
    await userEvent.upload(mediaInput(container), [a, b]);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(mockApi.upload).toHaveBeenCalledTimes(1);
    const form = mockApi.upload.mock.calls[0][0] as FormData;
    expect((form.getAll("files") as File[]).map((f) => f.name)).toEqual(["a.jpg", "b.mp4"]);
    expect(form.getAll("file_mtime")).toEqual(["1700000000000", "1700000001000"]);
    expect(await screen.findByText("Uploaded 2 files")).toBeInTheDocument();
    expect(screen.getByText("Uploaded 2 files, skipped 0, failed 0")).toBeInTheDocument();
  });

  it("uploads into no album by default", async () => {
    mockApi.upload.mockResolvedValue({ uploaded: ["a.mp4"], album_added: 0 });
    const { container } = render(<UploadPage />);
    await screen.findByRole("option", { name: "Family" });
    expect(screen.getByRole("combobox", { name: "Add to album" })).toHaveValue("");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText(/^Uploaded 3 files, skipped/)).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(3);
    for (const call of mockApi.upload.mock.calls) {
      expect(call).toHaveLength(2);
      expect(call[1]).toBeUndefined();
    }
    expect(screen.queryByText(/^Added .* to "/)).not.toBeInTheDocument();
  });

  it("offers the caller's albums, naming the owner of shared ones", async () => {
    render(<UploadPage />);
    const select = screen.getByRole("combobox", { name: "Add to album" });

    expect(await screen.findByRole("option", { name: "Family" })).toBeInTheDocument();
    expect(Array.from((select as HTMLSelectElement).options).map((o) => [o.value, o.text])).toEqual([
      ["", "None"],
      ["a1", "Family"],
      ["a2", "Iceland 2025 (by Bob)"],
    ]);
  });

  it("sends the chosen album with every batch and totals what the album gained", async () => {
    mockApi.upload
      .mockResolvedValueOnce({ uploaded: ["a.mp4"], skipped: [], failed: [], album_added: 1 })
      .mockResolvedValueOnce({ uploaded: [], skipped: ["b.mp4"], failed: [], album_added: 1 })
      .mockResolvedValueOnce({ uploaded: [], skipped: ["c.mp4"], failed: [], album_added: 0 });
    const { container } = render(<UploadPage />);
    await chooseAlbum("Iceland 2025 (by Bob)");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("Uploaded 1 file, skipped 2, failed 0")).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(3);
    expect(mockApi.upload.mock.calls.map((call) => call[1])).toEqual(["a2", "a2", "a2"]);
    expect(screen.getByText('Added 2 files to "Iceland 2025"')).toBeInTheDocument();
  });

  it("goes back to no album when None is chosen again", async () => {
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.selectOptions(screen.getByRole("combobox", { name: "Add to album" }), "None");
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText(/^Uploaded 2 files, skipped/)).toBeInTheDocument();
    expect(mockApi.upload.mock.calls[0][1]).toBeUndefined();
  });

  it("explains a 404 for the chosen album", async () => {
    mockApi.upload.mockRejectedValue(new ApiError("POST /media/upload failed: 404", 404));
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText('Could not upload into "Family": the album no longer exists, or you are no longer a member')
    ).toBeInTheDocument();
    expect(screen.getByText('Added 0 files to "Family"')).toBeInTheDocument();
    expect(screen.getByText("1 file failed")).toBeInTheDocument();
  });

  it("stops after the album 404s instead of sending every remaining batch", async () => {
    mockApi.upload.mockRejectedValue(new ApiError("POST /media/upload failed: 404", 404));
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText('Could not upload into "Family": the album no longer exists, or you are no longer a member')
    ).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(1);
    expect(screen.getByText("3 files failed")).toBeInTheDocument();
    for (const name of ["a.mp4", "b.mp4", "c.mp4"]) expect(screen.getByText(name)).toBeInTheDocument();
  });

  it("explains a vanished album before uploading anything", async () => {
    mockApi.listAlbums.mockResolvedValueOnce([family, iceland]).mockResolvedValue([iceland]);
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText('Could not upload into "Family": the album no longer exists, or you are no longer a member')
    ).toBeInTheDocument();
    expect(mockApi.upload).not.toHaveBeenCalled();
    // The album is gone from the choice, and the files stay selected for another try.
    const select = screen.getByRole("combobox", { name: "Add to album" });
    expect(select).toHaveValue("");
    expect(select).toBeEnabled();
    expect(within(select).queryByRole("option", { name: "Family" })).not.toBeInTheDocument();
    expect(screen.getByText("2 files selected")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload" })).toBeEnabled();
  });

  it("uploads into the album when its check cannot load the list", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mockApi.listAlbums.mockResolvedValueOnce([family, iceland]).mockRejectedValue(new Error("GET /albums failed: 500"));
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg"], skipped: [], failed: [], album_added: 1 });
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    // The backend checks membership itself.
    expect(await screen.findByText('Added 1 file to "Family"')).toBeInTheDocument();
    expect(mockApi.upload.mock.calls[0][1]).toBe("a1");
  });

  it("explains a network failure as a vanished album, and stops, when the album is gone", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    // Mount and the check before sending still list it; the check after the failure does not.
    mockApi.listAlbums
      .mockResolvedValueOnce([family, iceland])
      .mockResolvedValueOnce([family, iceland])
      .mockResolvedValue([iceland]);
    mockApi.upload.mockRejectedValue(new TypeError("Failed to fetch"));
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText('Could not upload into "Family": the album no longer exists, or you are no longer a member')
    ).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(1);
    expect(screen.getByText("3 files failed")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Add to album" })).toHaveValue("");
  });

  it("keeps sending batches after a failure while the album is still there", async () => {
    mockApi.upload
      .mockRejectedValueOnce(new Error("POST /media/upload failed: 502"))
      .mockResolvedValue({ uploaded: ["x.mp4"], skipped: [], failed: [], album_added: 1 });
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("POST /media/upload failed: 502")).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(3);
    expect(screen.getByText('Added 2 files to "Family"')).toBeInTheDocument();
    expect(screen.getByText("1 file failed")).toBeInTheDocument();
    expect(screen.queryByText(/no longer a member/)).not.toBeInTheDocument();
  });

  it("still uploads when the albums cannot be loaded", async () => {
    mockApi.listAlbums.mockRejectedValue(new Error("GET /albums failed: 500"));
    vi.spyOn(console, "error").mockImplementation(() => {});
    const { container } = render(<UploadPage />);

    expect(await screen.findByText("Could not load albums")).toBeInTheDocument();
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["None"]);
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(mockApi.upload.mock.calls[0][1]).toBeUndefined();
  });

  it("locks the album choice while uploading", async () => {
    let resolve!: (v: { uploaded: string[] }) => void;
    mockApi.upload.mockReturnValue(new Promise((r) => (resolve = r)));
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(screen.getByRole("combobox", { name: "Add to album" })).toBeDisabled();
    resolve({ uploaded: ["a.jpg"] });
    await waitFor(() => expect(screen.getByRole("combobox", { name: "Add to album" })).toBeEnabled());
  });

  it("lists duplicates the server skipped", async () => {
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg"], skipped: ["b.jpg", "c.jpg"], failed: null });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), [
      new File(["a"], "a.jpg", { type: "image/jpeg" }),
      new File(["b"], "b.jpg", { type: "image/jpeg" }),
      new File(["c"], "c.jpg", { type: "image/jpeg" }),
    ]);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("Uploaded 1 file, skipped 2, failed 0")).toBeInTheDocument();
    expect(screen.getByText("2 files skipped (already in your library)")).toBeInTheDocument();
    expect(screen.getByText("b.jpg")).toBeInTheDocument();
    expect(screen.getByText("c.jpg")).toBeInTheDocument();
    expect(screen.queryByText(/failed$/)).not.toBeInTheDocument();
  });

  it("counts files a backend without `skipped` left out as skipped", async () => {
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg"], failed: null });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), [
      new File(["a"], "a.jpg", { type: "image/jpeg" }),
      new File(["b"], "b.jpg", { type: "image/jpeg" }),
    ]);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("Uploaded 1 file, skipped 1, failed 0")).toBeInTheDocument();
    expect(screen.getByText("b.jpg")).toBeInTheDocument();
  });

  it("keeps long result lists inside a scrollable box", async () => {
    const names = Array.from({ length: 300 }, (_, i) => `img${i}.jpg`);
    mockApi.upload.mockResolvedValue({ uploaded: names });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), new File(["a"], "img0.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    const list = (await screen.findByText("img299.jpg")).closest("ul");
    expect(list).toHaveClass("max-h-48", "overflow-y-auto");
    expect(container.firstElementChild).toHaveClass("h-full", "overflow-y-auto");
  });

  it("shows the upload error", async () => {
    mockApi.upload.mockRejectedValue(new Error("POST /media/upload failed: 500"));
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByText("POST /media/upload failed: 500")).toBeInTheDocument();
  });

  it("splits large selections into several requests and lists failures", async () => {
    mockApi.upload
      .mockResolvedValueOnce({ uploaded: ["a.mp4"], failed: [] })
      .mockRejectedValueOnce(new Error("POST /media/upload failed: 502"))
      .mockResolvedValueOnce({ uploaded: [], failed: ["c.mp4"] });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("Uploaded 1 file")).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(3);
    expect(screen.getByText("2 files failed")).toBeInTheDocument();
    expect(screen.getByText("b.mp4")).toBeInTheDocument();
    expect(screen.getByText("c.mp4")).toBeInTheDocument();
    expect(screen.getByText("POST /media/upload failed: 502")).toBeInTheDocument();
  });

  it("does not send files over the per-request limit", async () => {
    const { container } = render(<UploadPage />);
    const huge = new File(["x"], "huge.mp4", { type: "video/mp4" });
    Object.defineProperty(huge, "size", { value: 5 * 1024 ** 3 });
    await userEvent.upload(mediaInput(container), huge);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("1 file failed")).toBeInTheDocument();
    expect(screen.getByText(/^huge\.mp4 \(larger than/)).toBeInTheDocument();
    expect(mockApi.upload).not.toHaveBeenCalled();
  });

  it("imports a GPS JSON file and shows the counts", async () => {
    render(<UploadPage />);
    const file = new File(["[]"], "points.json", { type: "application/json" });
    await userEvent.upload(screen.getByLabelText("GPS track JSON file"), file);
    expect(mockApi.importGpsTracks).toHaveBeenCalledWith(file);
    expect(await screen.findByText("Imported 5 point(s), 2 skipped, 1 invalid")).toBeInTheDocument();
  });

  it("shows the GPS import error", async () => {
    mockApi.importGpsTracks.mockRejectedValue(new Error("POST /gps-tracks/import failed: 400"));
    render(<UploadPage />);
    await userEvent.upload(
      screen.getByLabelText("GPS track JSON file"),
      new File(["x"], "points.json", { type: "application/json" })
    );
    expect(await screen.findByText("POST /gps-tracks/import failed: 400")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Choose JSON file" })).toBeEnabled());
  });

  it("shows the CLI scan note instead of the curl hint", () => {
    render(<UploadPage />);
    expect(screen.getByText("server scan --user <id>")).toBeInTheDocument();
    expect(screen.queryByText(/curl/)).not.toBeInTheDocument();
    expect(screen.queryByText(/ingest\/scan/)).not.toBeInTheDocument();
  });
});
