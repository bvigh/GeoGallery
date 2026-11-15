import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api, ApiError } from "@/api/client";
import { UploadPage } from "@/pages/UploadPage";
import { canHashFiles } from "@/lib/mediaHash";
import { formatCount } from "@/lib/numbers";
import { formatInstant, YEAR_OUT_OF_RANGE } from "@/lib/time";
import type { Album, GpsUploadResult, TimeReport, TrackFileReport, UploadResponse } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      upload: vi.fn(),
      knownHashes: vi.fn(),
      addToAlbum: vi.fn(),
      uploadGpsTracks: vi.fn(),
      listAlbums: vi.fn(),
      createAlbum: vi.fn(),
    },
  };
});

// mediaHash.test.ts checks the real hash; here every file's hash is "hash-<name>".
vi.mock("@/lib/mediaHash", () => ({
  canHashFiles: vi.fn(() => true),
  mediaHash: vi.fn(async (file: File) => `hash-${file.name}`),
}));

// CI runners are UTC, so the browser here is in a zone nine hours off it, with no daylight saving.
// formatInstant is the real one, counted, so a test can see which rows were formatted.
vi.mock("@/lib/time", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/time")>();
  return { ...actual, browserTimeZone: () => "Asia/Tokyo", formatInstant: vi.fn(actual.formatInstant) };
});

const mockApi = api as unknown as {
  upload: ReturnType<typeof vi.fn>;
  knownHashes: ReturnType<typeof vi.fn>;
  addToAlbum: ReturnType<typeof vi.fn>;
  uploadGpsTracks: ReturnType<typeof vi.fn>;
  listAlbums: ReturnType<typeof vi.fn>;
  createAlbum: ReturnType<typeof vi.fn>;
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
    last_added_at: null,
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

function albumButton() {
  return screen.getByRole("button", { name: /^Add to album:/ });
}

async function openPicker() {
  await userEvent.click(albumButton());
  return screen.findByRole("dialog");
}

async function chooseAlbum(name: string) {
  const dialog = await openPicker();
  await userEvent.click(await within(dialog).findByRole("radio", { name: new RegExp(`^${name}`) }));
  await userEvent.click(within(dialog).getByRole("button", { name: "Choose" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
}

// A report with every count 0 unless given.
function timeReport({
  bySource = {},
  byZone = {},
  ...rest
}: Partial<Omit<TimeReport, "by_source" | "by_zone">> & {
  bySource?: Partial<TimeReport["by_source"]>;
  byZone?: Partial<TimeReport["by_zone"]>;
} = {}): TimeReport {
  return {
    by_source: { exif_original: 0, exif_ifd0: 0, container: 0, mtime: 0, none: 0, ...bySource },
    by_zone: { gps: 0, clock_rule: 0, offset: 0, camera: 0, ...byZone },
    camera_zone: "Europe/Warsaw",
    assumed_zone_is_utc: false,
    file_date_names: [],
    ...rest,
  };
}

function trackFile(overrides: Partial<TrackFileReport> = {}): TrackFileReport {
  return {
    file: "walk.gpx",
    format: "gpx",
    points: 10,
    first: "2025-06-01T10:00:00Z",
    last: "2025-06-01T11:30:00Z",
    ...overrides,
  };
}

describe("UploadPage", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg", "b.mp4"] });
    mockApi.knownHashes.mockResolvedValue({});
    mockApi.addToAlbum.mockResolvedValue({ added: 0 });
    vi.mocked(canHashFiles).mockReturnValue(true);
    mockApi.uploadGpsTracks.mockResolvedValue({
      inserted: 5,
      skipped: 2,
      empty: ["route.gpx"],
      failed: [{ file: "broken.gpx", reason: "gpx: XML syntax error on line 1" }],
    });
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
    expect(albumButton()).toHaveAccessibleName("Add to album: None");
    // The albums load only when the picker opens.
    expect(mockApi.listAlbums).not.toHaveBeenCalled();
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

  it("offers the caller's albums and a new one, naming the owner of shared ones", async () => {
    render(<UploadPage />);
    const dialog = await openPicker();

    await within(dialog).findByRole("radio", { name: "Family" });
    expect(within(dialog).getAllByRole("radio").map((r) => r.closest("label")?.textContent)).toEqual([
      "Family",
      "Iceland 2025by Bob",
      "New album",
    ]);
  });

  it("shows the chosen album, naming the owner of a shared one, and preselects it next time", async () => {
    render(<UploadPage />);
    await chooseAlbum("Iceland 2025");
    expect(albumButton()).toHaveAccessibleName("Add to album: Iceland 2025");
    expect(albumButton()).toHaveTextContent("Iceland 2025 (by Bob)");

    const dialog = await openPicker();
    expect(await within(dialog).findByRole("radio", { name: /^Iceland 2025/ })).toBeChecked();
  });

  it("uploads into an album created in the picker", async () => {
    const trip = makeAlbum({ id: "a3", name: "Road trip" });
    mockApi.createAlbum.mockResolvedValue(trip);
    mockApi.listAlbums.mockResolvedValueOnce([family, iceland]).mockResolvedValue([family, iceland, trip]);
    mockApi.upload.mockResolvedValue({ uploaded: ["a.jpg"], skipped: [], failed: [], album_added: 1 });
    const { container } = render(<UploadPage />);
    const dialog = await openPicker();
    await within(dialog).findByRole("radio", { name: "Family" });
    await userEvent.type(within(dialog).getByRole("textbox", { name: "New album name" }), "Road trip");
    await userEvent.click(within(dialog).getByRole("button", { name: "Choose" }));

    expect(mockApi.createAlbum).toHaveBeenCalledWith("Road trip");
    await waitFor(() => expect(albumButton()).toHaveAccessibleName("Add to album: Road trip"));
    await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByText('Added 1 file to "Road trip"')).toBeInTheDocument();
    expect(mockApi.upload.mock.calls[0][1]).toBe("a3");
  });

  it("sends the chosen album with every batch and totals what the album gained", async () => {
    mockApi.upload
      .mockResolvedValueOnce({ uploaded: ["a.mp4"], skipped: [], failed: [], album_added: 1 })
      .mockResolvedValueOnce({ uploaded: [], skipped: ["b.mp4"], failed: [], album_added: 1 })
      .mockResolvedValueOnce({ uploaded: [], skipped: ["c.mp4"], failed: [], album_added: 0 });
    const { container } = render(<UploadPage />);
    await chooseAlbum("Iceland 2025");
    await userEvent.upload(mediaInput(container), bigVideos("a.mp4", "b.mp4", "c.mp4"));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(await screen.findByText("Uploaded 1 file, skipped 2, failed 0")).toBeInTheDocument();
    expect(mockApi.upload).toHaveBeenCalledTimes(3);
    expect(mockApi.upload.mock.calls.map((call) => call[1])).toEqual(["a2", "a2", "a2"]);
    expect(screen.getByText('Added 2 files to "Iceland 2025"')).toBeInTheDocument();
  });

  it("goes back to no album when the choice is cleared", async () => {
    const { container } = render(<UploadPage />);
    await chooseAlbum("Family");
    await userEvent.click(screen.getByRole("button", { name: "Upload into no album" }));
    expect(albumButton()).toHaveAccessibleName("Add to album: None");
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
    expect(albumButton()).toHaveAccessibleName("Add to album: None");
    expect(albumButton()).toBeEnabled();
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
    // The picker and the check before sending still list it; the check after the failure does not.
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
    expect(albumButton()).toHaveAccessibleName("Add to album: None");
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

    const dialog = await openPicker();
    expect(await within(dialog).findByText("Could not load albums")).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(albumButton()).toHaveAccessibleName("Add to album: None");
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

    expect(albumButton()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Upload into no album" })).toBeDisabled();
    resolve({ uploaded: ["a.jpg"] });
    await waitFor(() => expect(albumButton()).toBeEnabled());
  });

  describe("files already in the library", () => {
    function photos(...names: string[]): File[] {
      return names.map((name) => new File([name], name, { type: "image/jpeg" }));
    }

    async function uploadPhotos(container: HTMLElement, ...names: string[]) {
      await userEvent.upload(mediaInput(container), photos(...names));
      await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    }

    function sentNames(): string[] {
      return mockApi.upload.mock.calls.flatMap((call) => ((call[0] as FormData).getAll("files") as File[]).map((f) => f.name));
    }

    it("are not sent, and count as skipped", async () => {
      mockApi.knownHashes.mockResolvedValue({ "hash-a.jpg": "m-a" });
      mockApi.upload.mockResolvedValue({
        uploaded: ["b.jpg"],
        skipped: [],
        failed: [],
        time_report: timeReport({ bySource: { exif_original: 1 }, byZone: { offset: 1 } }),
      });
      const { container } = render(<UploadPage />);
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 1 file, skipped 1, failed 0")).toBeInTheDocument();
      expect(mockApi.knownHashes).toHaveBeenCalledWith(["hash-a.jpg", "hash-b.jpg"]);
      expect(sentNames()).toEqual(["b.jpg"]);
      expect(screen.getByText("1 file skipped (already in your library)")).toBeInTheDocument();
      expect(mockApi.addToAlbum).not.toHaveBeenCalled();
      // The server never read the known file, so the capture times are the sent file's only.
      expect(screen.getByText("Capture times: 1 from EXIF (1 photo offset)")).toBeInTheDocument();
    });

    it("send no request at all when every file is known", async () => {
      mockApi.knownHashes.mockResolvedValue({ "hash-a.jpg": "m-a", "hash-b.jpg": "m-b" });
      const { container } = render(<UploadPage />);
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 0 files, skipped 2, failed 0")).toBeInTheDocument();
      expect(mockApi.upload).not.toHaveBeenCalled();
    });

    it("go into the chosen album, and the totals add up", async () => {
      mockApi.knownHashes.mockResolvedValue({ "hash-a.jpg": "m-a", "hash-a-copy.jpg": "m-a" });
      mockApi.addToAlbum.mockResolvedValue({ added: 1 });
      mockApi.upload.mockResolvedValue({ uploaded: ["b.jpg"], skipped: [], failed: [], album_added: 1 });
      const { container } = render(<UploadPage />);
      await chooseAlbum("Family");
      await uploadPhotos(container, "a.jpg", "a-copy.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 1 file, skipped 2, failed 0")).toBeInTheDocument();
      expect(mockApi.addToAlbum).toHaveBeenCalledExactlyOnceWith("a1", { mediaIds: ["m-a"] });
      expect(sentNames()).toEqual(["b.jpg"]);
      expect(mockApi.upload.mock.calls[0][1]).toBe("a1");
      expect(screen.getByText('Added 2 files to "Family"')).toBeInTheDocument();
    });

    it("stop everything when the album is gone", async () => {
      mockApi.knownHashes.mockResolvedValue({ "hash-a.jpg": "m-a" });
      mockApi.addToAlbum.mockRejectedValue(new ApiError("POST /albums/a1/media failed: 404", 404));
      const { container } = render(<UploadPage />);
      await chooseAlbum("Family");
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(
        await screen.findByText('Could not upload into "Family": the album no longer exists, or you are no longer a member')
      ).toBeInTheDocument();
      expect(mockApi.upload).not.toHaveBeenCalled();
      expect(screen.getByText("2 files failed")).toBeInTheDocument();
    });

    it("fail alone when adding them to a listed album fails", async () => {
      mockApi.knownHashes.mockResolvedValue({ "hash-a.jpg": "m-a" });
      mockApi.addToAlbum.mockRejectedValue(new ApiError("POST /albums/a1/media failed: 500", 500));
      mockApi.upload.mockResolvedValue({ uploaded: ["b.jpg"], skipped: [], failed: [], album_added: 1 });
      const { container } = render(<UploadPage />);
      await chooseAlbum("Family");
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 1 file, skipped 0, failed 1")).toBeInTheDocument();
      expect(sentNames()).toEqual(["b.jpg"]);
      expect(screen.getByText("POST /albums/a1/media failed: 500")).toBeInTheDocument();
    });

    it("are sent anyway when the check fails", async () => {
      vi.spyOn(console, "warn").mockImplementation(() => {});
      mockApi.knownHashes.mockRejectedValue(new ApiError("POST /media/known-hashes failed: 404", 404));
      mockApi.upload.mockResolvedValue({ uploaded: ["b.jpg"], skipped: ["a.jpg"], failed: [] });
      const { container } = render(<UploadPage />);
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 1 file, skipped 1, failed 0")).toBeInTheDocument();
      expect(sentNames()).toEqual(["a.jpg", "b.jpg"]);
      expect(screen.queryByText(/known-hashes/)).not.toBeInTheDocument();
    });

    it("are sent anyway without Web Crypto", async () => {
      vi.mocked(canHashFiles).mockReturnValue(false);
      mockApi.upload.mockResolvedValue({ uploaded: ["b.jpg"], skipped: ["a.jpg"], failed: [] });
      const { container } = render(<UploadPage />);
      await uploadPhotos(container, "a.jpg", "b.jpg");

      expect(await screen.findByText("Uploaded 1 file, skipped 1, failed 0")).toBeInTheDocument();
      expect(mockApi.knownHashes).not.toHaveBeenCalled();
      expect(sentNames()).toEqual(["a.jpg", "b.jpg"]);
    });
  });

  describe("capture time receipt", () => {
    // Speaks of the files dated in the assumed zone only, allows that the zone may be right, and
    // says that only server retime moves files already stored.
    const UTC_WARNING_REST =
      "If the camera's clock did not show UTC, those times are off by its offset from UTC. An administrator sets " +
      "the camera zone with CAMERA_TIMEZONE. A new zone dates new uploads only: these files keep their times until " +
      "an administrator runs server retime, and uploading them again skips them as duplicates.";
    const UTC_WARNING = `The assumed camera zone is UTC all year, so the files dated in it were read as UTC. ${UTC_WARNING_REST}`;
    const UNSET_UTC_WARNING =
      "CAMERA_TIMEZONE is unset and the server's zone is UTC, so the files dated in it were read as UTC. " +
      UTC_WARNING_REST;

    // Every video is sent alone, and each request is answered in turn: with a report, with no
    // report (an older backend), or with an error.
    async function uploadAnswered(...answers: (TimeReport | undefined | Error)[]) {
      const names = answers.map((_, i) => `v${i}.mp4`);
      answers.forEach((answer, i) => {
        if (answer instanceof Error) {
          mockApi.upload.mockRejectedValueOnce(answer);
          return;
        }
        const response: UploadResponse = { uploaded: [names[i]], skipped: [], failed: [] };
        mockApi.upload.mockResolvedValueOnce(answer ? { ...response, time_report: answer } : response);
      });
      const { container } = render(<UploadPage />);
      await userEvent.upload(mediaInput(container), bigVideos(...names));
      await userEvent.click(screen.getByRole("button", { name: "Upload" }));
      await screen.findByText(/^Uploaded \d+ files?, skipped/);
    }

    it.each([
      {
        name: "every source and zone rule",
        report: timeReport({
          bySource: { exif_original: 30, exif_ifd0: 8, container: 3, mtime: 2, none: 1 },
          byZone: { gps: 10, clock_rule: 2, offset: 20, camera: 6 },
          file_date_names: ["a.jpg", "b.jpg"],
        }),
        line:
          "Capture times: 38 from EXIF (10 GPS clock, 2 camera clock rule, 20 photo offset, " +
          "6 assumed camera zone Europe/Warsaw), 3 from video container, 2 from file date, 1 with no capture time",
        general: "8 EXIF times come from the general date tag, which editing software can rewrite.",
      },
      {
        name: "EXIF read by one rule",
        report: timeReport({ bySource: { exif_original: 3 }, byZone: { offset: 3 } }),
        line: "Capture times: 3 from EXIF (3 photo offset)",
        general: null,
      },
      {
        name: "the general date tag alone",
        report: timeReport({ bySource: { exif_ifd0: 1 }, byZone: { gps: 1 } }),
        line: "Capture times: 1 from EXIF (1 GPS clock)",
        general: "1 EXIF time comes from the general date tag, which editing software can rewrite.",
      },
      {
        name: "the server's own zone",
        report: timeReport({
          bySource: { exif_original: 4 },
          byZone: { camera: 4 },
          camera_zone: "server zone (unset)",
        }),
        line: "Capture times: 4 from EXIF (4 assumed server zone)",
        general: null,
      },
      {
        name: "no EXIF at all",
        report: timeReport({ bySource: { container: 1, mtime: 2, none: 1 }, file_date_names: ["a.jpg", "b.jpg"] }),
        line: "Capture times: 1 from video container, 2 from file date, 1 with no capture time",
        general: null,
      },
    ])("names what dated the new files, leaving out what dated none: $name", async ({ report, line, general }) => {
      await uploadAnswered(report);

      expect(screen.getByText(line)).toBeInTheDocument();
      if (general) expect(screen.getByText(general)).toBeInTheDocument();
      else expect(screen.queryByText(/general date tag/)).not.toBeInTheDocument();
    });

    it("lists the files dated by their modified date behind a toggle", async () => {
      await uploadAnswered(
        timeReport({ bySource: { mtime: 2 }, file_date_names: ["IMG_0001.JPG", "IMG_0002.JPG"] })
      );

      const toggle = screen.getByRole("button", { name: "2 files from file date" });
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(toggle).toHaveClass("touch:min-h-11");
      expect(screen.queryByText("IMG_0001.JPG")).not.toBeInTheDocument();

      await userEvent.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "true");
      expect(screen.getByText("IMG_0001.JPG")).toBeInTheDocument();
      expect(screen.getByText("IMG_0002.JPG").closest("ul")).toHaveClass("max-h-48", "overflow-y-auto");

      await userEvent.click(toggle);
      expect(screen.queryByText("IMG_0001.JPG")).not.toBeInTheDocument();
    });

    it("adds up the reports of every batch", async () => {
      const utc = { camera_zone: "UTC" };
      await uploadAnswered(
        timeReport({ ...utc, bySource: { exif_original: 1 }, byZone: { offset: 1 } }),
        timeReport({ ...utc, bySource: { mtime: 1 }, file_date_names: ["v1.mp4"] }),
        // Only this batch had a file read in the camera zone, so only its report says the zone is UTC.
        timeReport({ ...utc, bySource: { exif_ifd0: 1 }, byZone: { camera: 1 }, assumed_zone_is_utc: true }),
        new Error("POST /media/upload failed: 502"), // a failed request reports nothing
        timeReport({ ...utc, bySource: { mtime: 1 }, file_date_names: ["v4.mp4"] }),
        timeReport({ ...utc, bySource: { exif_original: 1 }, byZone: { offset: 1 } })
      );

      expect(
        screen.getByText("Capture times: 3 from EXIF (2 photo offset, 1 assumed camera zone UTC), 2 from file date")
      ).toBeInTheDocument();
      expect(
        screen.getByText("1 EXIF time comes from the general date tag, which editing software can rewrite.")
      ).toBeInTheDocument();
      expect(screen.getByText(UTC_WARNING)).toBeInTheDocument();
      const toggle = screen.getByRole("button", { name: "2 files from file date" });
      await userEvent.click(toggle);
      const names = within(toggle.nextElementSibling as HTMLElement).getAllByRole("listitem");
      expect(names.map((li) => li.textContent)).toEqual(["v1.mp4", "v4.mp4"]);
    });

    it.each([
      {
        name: "a camera zone that is UTC all year",
        report: timeReport({
          bySource: { exif_original: 1 },
          byZone: { camera: 1 },
          camera_zone: "Etc/UTC",
          assumed_zone_is_utc: true,
        }),
        line: "Capture times: 1 from EXIF (1 assumed camera zone Etc/UTC)",
        warning: UTC_WARNING,
      },
      {
        // The evidence view's words for the same case.
        name: "the server's own zone, UTC all year",
        report: timeReport({
          bySource: { exif_original: 1 },
          byZone: { camera: 1 },
          camera_zone: "server zone (unset)",
          assumed_zone_is_utc: true,
        }),
        line: "Capture times: 1 from EXIF (1 assumed server zone)",
        warning: UNSET_UTC_WARNING,
      },
      {
        name: "a camera zone at UTC for part of the year",
        report: timeReport({
          bySource: { exif_original: 1 },
          byZone: { camera: 1 },
          camera_zone: "Europe/London",
          assumed_zone_is_utc: false,
        }),
        line: "Capture times: 1 from EXIF (1 assumed camera zone Europe/London)",
        warning: null,
      },
    ])("warns about one file only when its zone is UTC all year: $name", async ({ report, line, warning }) => {
      await uploadAnswered(report);

      expect(screen.getByText(line)).toBeInTheDocument();
      if (warning) {
        expect(screen.getByText(warning)).toBeInTheDocument();
        expect(screen.getByText(/server retime/)).toBe(screen.getByText(warning));
      } else {
        expect(screen.queryByText(/read as UTC/)).not.toBeInTheDocument();
      }
    });

    it("says that only the files dated in the assumed zone were read as UTC", async () => {
      await uploadAnswered(
        timeReport({
          bySource: { exif_original: 9 },
          byZone: { gps: 2, offset: 3, camera: 4 },
          camera_zone: "Etc/UTC",
          assumed_zone_is_utc: true,
        })
      );

      expect(
        screen.getByText("Capture times: 9 from EXIF (2 GPS clock, 3 photo offset, 4 assumed camera zone Etc/UTC)")
      ).toBeInTheDocument();
      // The GPS clock and the offset tag dated five of them, which the warning must not take in.
      expect(screen.getByText(UTC_WARNING).textContent).not.toMatch(/\b(these|all|the) files were read as UTC/i);
    });

    it("says nothing when no file was new", async () => {
      mockApi.upload.mockResolvedValue({ uploaded: [], skipped: ["a.jpg"], failed: [], time_report: timeReport() });
      const { container } = render(<UploadPage />);
      await userEvent.upload(mediaInput(container), new File(["a"], "a.jpg", { type: "image/jpeg" }));
      await userEvent.click(screen.getByRole("button", { name: "Upload" }));

      expect(await screen.findByText("Uploaded 0 files, skipped 1, failed 0")).toBeInTheDocument();
      expect(screen.queryByText(/^Capture times/)).not.toBeInTheDocument();
    });

    it("says nothing from a backend that does not report capture times", async () => {
      await uploadAnswered(undefined);
      expect(screen.getByText("Uploaded 1 file, skipped 0, failed 0")).toBeInTheDocument();
      expect(screen.queryByText(/^Capture times/)).not.toBeInTheDocument();
    });
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

  it("imports GPS track files and lists empty and failed files", async () => {
    render(<UploadPage />);
    const files = [
      new File(["<gpx/>"], "walk.gpx"),
      new File(["$GPRMC"], "FILE240615-120000.NMEA"),
      new File(["{}"], "Zeitachse.json.gz"),
    ];
    await userEvent.upload(screen.getByLabelText("GPS track files"), files);
    expect(mockApi.uploadGpsTracks).toHaveBeenCalledTimes(1);
    expect(mockApi.uploadGpsTracks).toHaveBeenCalledWith(files);
    expect(await screen.findByText("Imported 5 point(s), 2 already imported")).toBeInTheDocument();
    expect(screen.getByText("1 file without timestamped points")).toBeInTheDocument();
    expect(screen.getByText("route.gpx")).toBeInTheDocument();
    expect(screen.getByText("1 file failed")).toBeInTheDocument();
    expect(screen.getByText("broken.gpx: gpx: XML syntax error on line 1")).toBeInTheDocument();
  });

  it("sends many track files in batches of at most 500 and adds up the results", async () => {
    render(<UploadPage />);
    const files = Array.from({ length: 501 }, (_, i) => new File(["[]"], `${i}.location_data.json`));
    await userEvent.upload(screen.getByLabelText("GPS track files"), files);
    await waitFor(() => expect(mockApi.uploadGpsTracks).toHaveBeenCalledTimes(2));
    expect(mockApi.uploadGpsTracks.mock.calls[0][0]).toHaveLength(500);
    expect(mockApi.uploadGpsTracks.mock.calls[1][0]).toHaveLength(1);
    expect(await screen.findByText("Imported 10 point(s), 4 already imported")).toBeInTheDocument();
  });

  it("does not send a track file over the size limit", async () => {
    render(<UploadPage />);
    const huge = new File(["x"], "Zeitachse.json");
    Object.defineProperty(huge, "size", { value: 300 * 1024 ** 2 });
    const small = new File(["<gpx/>"], "walk.gpx");
    await userEvent.upload(screen.getByLabelText("GPS track files"), [huge, small]);
    await waitFor(() => expect(mockApi.uploadGpsTracks).toHaveBeenCalledWith([small]));
    expect(await screen.findByText("Zeitachse.json: larger than 255 MiB")).toBeInTheDocument();
  });

  it("shows the GPS import error and lists the batch's files as failed", async () => {
    mockApi.uploadGpsTracks.mockRejectedValue(new Error("POST /gps-tracks/upload failed: 413"));
    render(<UploadPage />);
    await userEvent.upload(screen.getByLabelText("GPS track files"), new File(["x"], "walk.gpx"));
    expect(await screen.findByText("POST /gps-tracks/upload failed: 413")).toBeInTheDocument();
    expect(screen.getByText("1 file failed")).toBeInTheDocument();
    expect(screen.getByText("walk.gpx")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Choose track files" })).toBeEnabled());
  });

  describe("track file receipt", () => {
    const NOW = "2026-11-15T12:00:00Z";
    const DAY_MS = 86400 * 1000;

    afterEach(() => {
      vi.useRealTimers();
    });

    function afterNow(ms: number): string {
      return new Date(Date.parse(NOW) + ms).toISOString();
    }

    // A response that imported every point of these files.
    function imported(...files: TrackFileReport[]): GpsUploadResult {
      return { inserted: files.reduce((sum, f) => sum + f.points, 0), skipped: 0, empty: [], failed: [], files };
    }

    async function importTracks(...names: string[]) {
      render(<UploadPage />);
      await userEvent.upload(
        screen.getByLabelText("GPS track files"),
        names.map((name) => new File(["x"], name))
      );
      await screen.findByText(/^Imported .+ point\(s\)/);
    }

    function receipt(): HTMLElement {
      return screen.getByText(/ with timestamped points$/).parentElement!;
    }

    // The file names in the receipt, in its order.
    function listed(): (string | null | undefined)[] {
      return within(receipt())
        .getAllByRole("listitem")
        .map((li) => li.querySelector("p")?.textContent);
    }

    function row(name: string): HTMLElement {
      return within(receipt()).getByText(name).closest("li")!;
    }

    // The value a file's row gives under a label.
    function fact(name: string, label: string): string | null | undefined {
      return within(row(name)).getByText(label).nextElementSibling?.textContent;
    }

    it("lists each file with its format, points, and first and last time in UTC and the browser zone", async () => {
      const first = "2025-06-01T10:00:00Z";
      const last = "2025-06-01T23:30:00Z";
      mockApi.uploadGpsTracks.mockResolvedValue(
        imported(trackFile({ file: "<b>ride</b>.gpx", format: "gpx", points: 1234, first, last }))
      );
      await importTracks("<b>ride</b>.gpx");

      expect(within(receipt()).getByText("1 file with timestamped points")).toBeInTheDocument();
      expect(within(row("<b>ride</b>.gpx")).getByText(`gpx · ${formatCount(1234)} points`)).toBeInTheDocument();
      expect(fact("<b>ride</b>.gpx", "First, UTC")).toBe(formatInstant(first, "UTC"));
      expect(fact("<b>ride</b>.gpx", "First, your browser")).toBe(formatInstant(first, "Asia/Tokyo"));
      expect(fact("<b>ride</b>.gpx", "Last, UTC")).toBe(formatInstant(last, "UTC"));
      expect(fact("<b>ride</b>.gpx", "Last, your browser")).toBe(formatInstant(last, "Asia/Tokyo"));
      // A file name is the owner's upload, only ever rendered as text.
      expect(receipt().querySelector("b")).toBeNull();
      expect(screen.queryByText(/in the future/)).not.toBeInTheDocument();
    });

    it("warns about a file whose last point is more than a day ahead, and lists it first", async () => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date(NOW));
      mockApi.uploadGpsTracks.mockResolvedValue(
        imported(
          trackFile({ file: "edge.gpx", last: afterNow(DAY_MS) }),
          trackFile({ file: "ahead.gpx", last: afterNow(DAY_MS + 1000) }),
          // A receiver that lost its GPS week dates its log 1024 weeks ahead.
          trackFile({ file: "week.nmea", format: "nmea", last: afterNow(1024 * 7 * DAY_MS) })
        )
      );
      await importTracks("edge.gpx", "ahead.gpx", "week.nmea");

      expect(listed()).toEqual(["ahead.gpx", "week.nmea", "edge.gpx"]);
      expect(
        within(receipt()).getByText(
          "2 of them end more than a day in the future, so their times are probably wrong. They are listed first."
        )
      ).toBeInTheDocument();
      expect(
        within(row("ahead.gpx")).getByText("Its last point is 1 d 1 s in the future, so its times are probably wrong.")
      ).toBeInTheDocument();
      // Years, not 7168 days.
      expect(
        within(row("week.nmea")).getByText(
          "Its last point is about 20 years in the future, so its times are probably wrong."
        )
      ).toBeInTheDocument();
      expect(within(row("edge.gpx")).queryByText(/in the future/)).not.toBeInTheDocument();
    });

    it.each([
      {
        name: "the only file",
        files: [trackFile({ file: "week.nmea", last: afterNow(9 * DAY_MS) })],
        summary: null, // its own row says it
      },
      {
        name: "every file",
        files: [
          trackFile({ file: "a.nmea", last: afterNow(9 * DAY_MS) }),
          trackFile({ file: "b.nmea", last: afterNow(9 * DAY_MS) }),
        ],
        summary: "2 of them end more than a day in the future, so their times are probably wrong.",
      },
      {
        name: "one of several",
        files: [trackFile({ file: "a.gpx" }), trackFile({ file: "b.nmea", last: afterNow(9 * DAY_MS) })],
        summary: "1 of them ends more than a day in the future, so its times are probably wrong. It is listed first.",
      },
    ])("sums up the files dated ahead above the list: $name", async ({ files, summary }) => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date(NOW));
      mockApi.uploadGpsTracks.mockResolvedValue(imported(...files));
      await importTracks(...files.map((f) => f.file));

      if (summary) expect(within(receipt()).getByText(summary)).toBeInTheDocument();
      else expect(within(receipt()).queryByText(/ of them ends? /)).not.toBeInTheDocument();
    });

    it("shows 200 files at a time, those dated ahead first, and formats only the files shown", async () => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date(NOW));
      // 900 files go in two batches; 450 of them have points, and only the last one is dated ahead.
      const files = Array.from({ length: 450 }, (_, i) =>
        i === 449 ? trackFile({ file: "week.nmea", last: afterNow(9 * DAY_MS) }) : trackFile({ file: `t${i}.gpx` })
      );
      mockApi.uploadGpsTracks
        .mockResolvedValueOnce(imported(...files.slice(0, 300)))
        .mockResolvedValueOnce(imported(...files.slice(300)));
      await importTracks(...Array.from({ length: 900 }, (_, i) => `${i}.gpx`));

      expect(mockApi.uploadGpsTracks).toHaveBeenCalledTimes(2);
      expect(within(receipt()).getByText("450 files with timestamped points")).toBeInTheDocument();
      expect(
        within(receipt()).getByText(
          "1 of them ends more than a day in the future, so its times are probably wrong. It is listed first."
        )
      ).toBeInTheDocument();
      expect(listed()).toEqual(["week.nmea", ...files.slice(0, 199).map((f) => f.file)]);
      // Each file shown formats its first and last time in two zones; no other file is formatted.
      expect(formatInstant).toHaveBeenCalledTimes(200 * 4);
      // The page scrolls, not a box inside it that would cut a file off.
      expect(within(receipt()).getByRole("list").className).not.toMatch(/max-h-|overflow-y-(auto|scroll)/);

      const more = within(receipt()).getByRole("button", { name: "Show more" });
      expect(more).toHaveClass("touch:min-h-11");
      expect(within(receipt()).getByText("200 of 450 shown")).toBeInTheDocument();
      await userEvent.click(more);
      expect(listed()).toHaveLength(400);
      expect(within(receipt()).getByText("400 of 450 shown")).toBeInTheDocument();
      // The files already shown are not formatted again.
      expect(formatInstant).toHaveBeenCalledTimes(400 * 4);
      // The focus goes to the first file added.
      expect(document.activeElement).toBe(row("t199.gpx"));

      await userEvent.click(more);
      expect(listed()).toEqual(["week.nmea", ...files.slice(0, 449).map((f) => f.file)]);
      expect(within(receipt()).queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
      expect(within(receipt()).queryByText(/ shown$/)).not.toBeInTheDocument();
      // The button is gone, so the focus must not stay with it.
      expect(document.activeElement).toBe(row("t399.gpx"));
    });

    it("lists the files after the failed ones and the errors, which a long list would push out of sight", async () => {
      mockApi.uploadGpsTracks
        .mockResolvedValueOnce({
          ...imported(trackFile({ file: "a.gpx" })),
          empty: ["e.gpx"],
          failed: [{ file: "f.gpx", reason: "gpx: XML syntax error on line 1" }],
        })
        .mockRejectedValueOnce(new Error("POST /gps-tracks/upload failed: 502"));
      await importTracks(...Array.from({ length: 501 }, (_, i) => `${i}.gpx`));

      const header = screen.getByText("1 file with timestamped points");
      for (const before of [
        screen.getByText("1 file without timestamped points"),
        screen.getByText("2 files failed"),
        screen.getByText("POST /gps-tracks/upload failed: 502"),
      ]) {
        expect(before.compareDocumentPosition(header) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
      }
    });

    it("reads a time JSON cannot carry as out of range, as the Checks view does", async () => {
      mockApi.uploadGpsTracks.mockResolvedValue(
        imported(trackFile({ file: "pin.json", format: "geogallery", points: 1, first: null, last: null }))
      );
      await importTracks("pin.json");

      expect(within(row("pin.json")).getByText("geogallery · 1 point")).toBeInTheDocument();
      expect(fact("pin.json", "First")).toBe(YEAR_OUT_OF_RANGE);
      expect(fact("pin.json", "Last")).toBe(YEAR_OUT_OF_RANGE);
      expect(within(row("pin.json")).queryByText("First, UTC")).not.toBeInTheDocument();
      expect(screen.queryByText(/in the future/)).not.toBeInTheDocument();
    });

    it("adds up the files of every batch", async () => {
      // Three batches of at most 500 files; the second fails.
      mockApi.uploadGpsTracks
        .mockResolvedValueOnce(imported(trackFile({ file: "0.json" })))
        .mockRejectedValueOnce(new Error("POST /gps-tracks/upload failed: 502"))
        .mockResolvedValueOnce(imported(trackFile({ file: "1000.json" })));
      await importTracks(...Array.from({ length: 1001 }, (_, i) => `${i}.json`));

      expect(within(receipt()).getByText("2 files with timestamped points")).toBeInTheDocument();
      expect(listed()).toEqual(["0.json", "1000.json"]);
    });

    it("shows no files from a backend that does not report them", async () => {
      await importTracks("walk.gpx");
      expect(screen.getByText("Imported 5 point(s), 2 already imported")).toBeInTheDocument();
      expect(screen.queryByText(/ with timestamped points$/)).not.toBeInTheDocument();
    });
  });

  it("groups the digits of large counts in both receipts", async () => {
    // Else this test could not tell a grouped count from a bare one.
    expect(formatCount(1234)).not.toBe("1234");
    const count = formatCount;
    mockApi.upload.mockResolvedValue({
      uploaded: Array.from({ length: 1234 }, (_, i) => `img${i}.jpg`),
      skipped: Array.from({ length: 1001 }, (_, i) => `dup${i}.jpg`),
      failed: [],
      time_report: timeReport({ bySource: { exif_original: 34, exif_ifd0: 1200 }, byZone: { offset: 1234 } }),
    });
    mockApi.uploadGpsTracks.mockResolvedValue({
      inserted: 29494,
      skipped: 1000,
      empty: [],
      failed: [],
      files: [trackFile({ file: "drive.nmea", format: "nmea", points: 27558 })],
    });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), new File(["a"], "img0.jpg", { type: "image/jpeg" }));
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));

    expect(
      await screen.findByText(`Uploaded ${count(1234)} files, skipped ${count(1001)}, failed 0`)
    ).toBeInTheDocument();
    expect(screen.getByText(`${count(1001)} files skipped (already in your library)`)).toBeInTheDocument();
    expect(
      screen.getByText(`Capture times: ${count(1234)} from EXIF (${count(1234)} photo offset)`)
    ).toBeInTheDocument();
    expect(
      screen.getByText(`${count(1200)} EXIF times come from the general date tag, which editing software can rewrite.`)
    ).toBeInTheDocument();

    await userEvent.upload(screen.getByLabelText("GPS track files"), new File(["x"], "drive.nmea"));
    expect(
      await screen.findByText(`Imported ${count(29494)} point(s), ${count(1000)} already imported`)
    ).toBeInTheDocument();
    expect(screen.getByText(`nmea · ${count(27558)} points`)).toBeInTheDocument();
  });

  it("shows the CLI scan note instead of the curl hint", () => {
    render(<UploadPage />);
    expect(screen.getByText("server scan --user <id>")).toBeInTheDocument();
    expect(screen.queryByText(/curl/)).not.toBeInTheDocument();
    expect(screen.queryByText(/ingest\/scan/)).not.toBeInTheDocument();
  });

  it("breaks long file names in the result lists instead of widening the page", async () => {
    const name = "PXL_20240701_101112345.NIGHT.MOTION-01.COVER.jpg";
    mockApi.upload.mockResolvedValue({ uploaded: [name] });
    const { container } = render(<UploadPage />);
    await userEvent.upload(mediaInput(container), [new File(["a"], name, { type: "image/jpeg" })]);
    await userEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByText(name)).toHaveClass("[overflow-wrap:anywhere]");
  });

  it("makes the album choice finger-sized on touch", async () => {
    render(<UploadPage />);
    expect(albumButton()).toHaveClass("touch:min-h-11");
    await chooseAlbum("Family");
    expect(screen.getByRole("button", { name: "Upload into no album" })).toHaveClass("touch:h-11", "touch:w-11");
  });
});
