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
    expect(await screen.findByText("Uploaded 2 file(s)")).toBeInTheDocument();
  });

  it("shows the upload error", async () => {
    mockApi.upload.mockRejectedValue(new Error("POST /media/upload failed: 500"));
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByText("POST /media/upload failed: 500")).toBeInTheDocument();
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
