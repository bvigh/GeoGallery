import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { api } from "@/api/client";
import { UploadPage } from "@/pages/UploadPage";

vi.mock("@/api/client", () => ({
  api: { upload: vi.fn(), importGpsTracks: vi.fn() },
}));

const mockApi = api as unknown as {
  upload: ReturnType<typeof vi.fn>;
  importGpsTracks: ReturnType<typeof vi.fn>;
};

function mediaInput(container: HTMLElement): HTMLInputElement {
  return container.querySelector('input[type="file"][multiple]') as HTMLInputElement;
}

describe("UploadPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg", "b.mp4"] });
    mockApi.importGpsTracks.mockResolvedValue({ inserted: 5, skipped: 2, errors: [{ row: 3, reason: "bad lat" }] });
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
    const big = (name: string) => {
      const f = new File(["x"], name, { type: "video/mp4" });
      Object.defineProperty(f, "size", { value: 400 * 1024 ** 2 });
      return f;
    };
    await userEvent.upload(mediaInput(container), [big("a.mp4"), big("b.mp4"), big("c.mp4")]);
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
