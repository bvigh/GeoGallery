import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "@/api/client";
import { CameraClockRules } from "@/components/settings/CameraClockRules";
import type { CameraClockRule, LibraryCamera } from "@/types";

vi.mock("@/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client")>();
  return {
    ApiError: actual.ApiError,
    api: {
      listCameraClockRules: vi.fn(),
      createCameraClockRule: vi.fn(),
      updateCameraClockRule: vi.fn(),
      deleteCameraClockRule: vi.fn(),
      listCameras: vi.fn(),
    },
  };
});

const mockApi = api as unknown as {
  listCameraClockRules: ReturnType<typeof vi.fn>;
  createCameraClockRule: ReturnType<typeof vi.fn>;
  updateCameraClockRule: ReturnType<typeof vi.fn>;
  deleteCameraClockRule: ReturnType<typeof vi.fn>;
  listCameras: ReturnType<typeof vi.fn>;
};

function makeRule(overrides: Partial<CameraClockRule> = {}): CameraClockRule {
  return {
    id: "r1",
    make: "Panasonic",
    model: "DC-TZ200",
    utc_offset_min: 120,
    starts_on: null,
    ends_on: null,
    note: "",
    created_at: "2026-11-01T10:00:00Z",
    updated_at: "2026-11-01T10:00:00Z",
    ...overrides,
  };
}

const tz200: LibraryCamera = {
  make: "Panasonic",
  model: "DC-TZ200",
  items: 595,
  first_taken_at: "2019-03-13T17:28:15Z",
  last_taken_at: "2022-08-01T09:00:00Z",
};

const phone: LibraryCamera = {
  make: "samsung",
  model: "SM-S921B",
  items: 1,
  first_taken_at: "2024-05-01T09:00:00Z",
  last_taken_at: "2024-05-01T09:00:00Z",
};

const TZ200_LABEL = "Panasonic DC-TZ200 · 595 items · 2019–2022";

function rowOf(text: string): HTMLElement {
  return screen.getByText(text).closest("li")!;
}

async function openAddDialog() {
  await userEvent.click(screen.getByRole("button", { name: "Add rule" }));
  return screen.findByRole("dialog");
}

// The dialog's camera select is disabled until the library cameras have loaded.
async function camerasLoaded(dialog: HTMLElement) {
  await waitFor(() => expect(within(dialog).getByLabelText("Camera")).toBeEnabled());
}

describe("CameraClockRules", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockApi.listCameraClockRules.mockResolvedValue([]);
    mockApi.listCameras.mockResolvedValue([tz200, phone]);
    mockApi.createCameraClockRule.mockImplementation(async (input) => makeRule({ ...input, id: "r9" }));
    mockApi.updateCameraClockRule.mockImplementation(async (id, input) => makeRule({ ...input, id }));
    mockApi.deleteCameraClockRule.mockResolvedValue(undefined);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("the list", () => {
    it("explains what a rule does, and that stored items keep their times", async () => {
      render(<CameraClockRules />);
      expect(screen.getByRole("heading", { name: "Camera clocks" })).toBeInTheDocument();
      expect(screen.getByText(/stayed on summer time all year/)).toBeInTheDocument();
      expect(screen.getByText(/New uploads and scans from that camera are then read at that offset/)).toBeInTheDocument();
      expect(screen.getByText(/keep their times until you edit or shift them, or an administrator runs/)).toBeInTheDocument();
      expect(await screen.findByText("No clock rules yet.")).toBeInTheDocument();
    });

    it("shows a loading line until the rules arrive", async () => {
      let answer!: (rules: CameraClockRule[]) => void;
      mockApi.listCameraClockRules.mockReturnValue(new Promise((resolve) => (answer = resolve)));
      render(<CameraClockRules />);
      expect(screen.getByText("Loading clock rules…")).toBeInTheDocument();
      answer([makeRule()]);
      expect(await screen.findByText("Panasonic · DC-TZ200")).toBeInTheDocument();
      expect(screen.queryByText("Loading clock rules…")).not.toBeInTheDocument();
    });

    it("lists each rule with its camera, offset, period and note", async () => {
      mockApi.listCameraClockRules.mockResolvedValue([
        makeRule({ id: "r1", note: "Never switched to winter time" }),
        makeRule({ id: "r2", make: "Arashi Vision", model: "Insta360 ONE RS", utc_offset_min: 60, starts_on: "2022-06-01", ends_on: "2022-09-30" }),
        makeRule({ id: "r3", make: "Canon", model: "Canon EOS R5", utc_offset_min: -330, starts_on: "2018-07-01" }),
        makeRule({ id: "r4", make: "Sony", model: "ILCE-7M3", utc_offset_min: 0, ends_on: "2019-03-31" }),
        makeRule({ id: "r5", make: "Sony", model: "DSC-RX100", utc_offset_min: 345, starts_on: "2020-01-01", ends_on: "2020-01-01" }),
      ]);
      render(<CameraClockRules />);

      const tz = (await screen.findByText("Panasonic · DC-TZ200")).closest("li")!;
      expect(within(tz).getByText("Clock showed UTC+02:00 · always")).toBeInTheDocument();
      expect(within(tz).getByText("Never switched to winter time")).toBeInTheDocument();
      expect(within(rowOf("Arashi Vision · Insta360 ONE RS")).getByText("Clock showed UTC+01:00 · 2022-06-01 – 2022-09-30")).toBeInTheDocument();
      // Make and model as stored, even when the model starts with the make.
      expect(within(rowOf("Canon · Canon EOS R5")).getByText("Clock showed UTC-05:30 · from 2018-07-01")).toBeInTheDocument();
      expect(within(rowOf("Sony · ILCE-7M3")).getByText("Clock showed UTC+00:00 · until 2019-03-31")).toBeInTheDocument();
      expect(within(rowOf("Sony · DSC-RX100")).getByText("Clock showed UTC+05:45 · on 2020-01-01")).toBeInTheDocument();

      expect(within(tz).getByRole("button", { name: "Edit rule for Panasonic · DC-TZ200, always" })).toHaveClass("touch:min-h-11");
      expect(within(tz).getByRole("button", { name: "Delete rule for Panasonic · DC-TZ200, always" })).toHaveClass("touch:min-h-11");
    });

    it("tells apart two rules that one merged camera name would show alike", async () => {
      // A rule matches the EXIF Model exactly: Canon files write "Canon EOS R5", so the first rule
      // never applies, and the list must not make it look like the second.
      mockApi.listCameraClockRules.mockResolvedValue([
        makeRule({ id: "r1", make: "Canon", model: "EOS R5" }),
        makeRule({ id: "r2", make: "Canon", model: "Canon EOS R5" }),
      ]);
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
      render(<CameraClockRules />);
      expect(await screen.findByText("Canon · EOS R5")).toBeInTheDocument();
      expect(screen.getByText("Canon · Canon EOS R5")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Edit rule for Canon · EOS R5, always" })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Edit rule for Canon · Canon EOS R5, always" })).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Delete rule for Canon · EOS R5, always" }));
      expect(confirmSpy).toHaveBeenLastCalledWith(expect.stringMatching(/^Delete the clock rule for Canon · EOS R5 \(always\)\?/));
    });

    it("says when the rules could not be loaded, and tries again", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listCameraClockRules.mockRejectedValueOnce(new Error("boom"));
      render(<CameraClockRules />);
      expect(await screen.findByText("Could not load your clock rules.")).toBeInTheDocument();
      expect(screen.queryByText("Loading clock rules…")).not.toBeInTheDocument();

      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      await userEvent.click(screen.getByRole("button", { name: "Try again" }));
      expect(await screen.findByText("Panasonic · DC-TZ200")).toBeInTheDocument();
      expect(screen.queryByText("Could not load your clock rules.")).not.toBeInTheDocument();
    });

    it("keeps only the latest load when two overlap", async () => {
      const answers: ((rules: CameraClockRule[]) => void)[] = [];
      mockApi.listCameraClockRules.mockImplementation(() => new Promise((resolve) => answers.push(resolve)));
      render(<CameraClockRules />);
      // A delete starts the second load while the first is still out.
      vi.spyOn(window, "confirm").mockReturnValue(true);
      answers[0]([makeRule({ id: "r1" }), makeRule({ id: "r2", model: "DC-TZ90" })]);
      await userEvent.click(await screen.findByRole("button", { name: "Delete rule for Panasonic · DC-TZ90, always" }));
      await waitFor(() => expect(answers).toHaveLength(2));
      // Then a third, from a save, answers first; the older second load must not undo it.
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+03:00");
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      await waitFor(() => expect(answers).toHaveLength(3));
      answers[2]([makeRule({ id: "r1" }), makeRule({ id: "r9", model: "DC-TZ99" })]);
      expect(await screen.findByText("Panasonic · DC-TZ99")).toBeInTheDocument();
      answers[1]([makeRule({ id: "r1" })]);
      await new Promise((r) => setTimeout(r, 0));
      expect(screen.getByText("Panasonic · DC-TZ99")).toBeInTheDocument();
    });

    it("stops adding at 100 rules", async () => {
      mockApi.listCameraClockRules.mockResolvedValue(
        Array.from({ length: 100 }, (_, i) => makeRule({ id: `r${i}`, model: `Model ${i}` }))
      );
      render(<CameraClockRules />);
      expect(await screen.findByText("You have 100 rules, the most allowed. Delete one to add another.")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Add rule" })).toBeDisabled();
    });
  });

  describe("adding a rule", () => {
    it("creates one for a camera from the library", async () => {
      render(<CameraClockRules />);
      await screen.findByText("No clock rules yet.");
      const dialog = await openAddDialog();
      expect(within(dialog).getByRole("heading", { name: "Add clock rule" })).toBeInTheDocument();
      // Videos dated from their container time are not read at the rule's offset.
      expect(within(dialog).getByText(/read at that offset wherever their time comes from its EXIF clock/)).toBeInTheDocument();
      await camerasLoaded(dialog);

      const cameraSelect = within(dialog).getByLabelText("Camera") as HTMLSelectElement;
      expect(Array.from(cameraSelect.options).map((o) => o.textContent)).toEqual([
        "Choose a camera",
        TZ200_LABEL,
        "samsung SM-S921B · 1 item · 2024",
        "Other camera",
      ]);
      // Nothing is chosen for the user, and a camera and an offset are both required.
      const submit = within(dialog).getByRole("button", { name: "Add rule" });
      expect(submit).toBeDisabled();
      await userEvent.selectOptions(cameraSelect, TZ200_LABEL);
      expect(within(dialog).queryByLabelText("Make")).not.toBeInTheDocument();
      expect(submit).toBeDisabled();

      const offsetSelect = within(dialog).getByLabelText("Clock showed") as HTMLSelectElement;
      const offsets = Array.from(offsetSelect.options).filter((o) => !o.disabled).map((o) => o.textContent);
      expect(offsets).toHaveLength(105);
      expect(offsets[0]).toBe("UTC-12:00");
      expect(offsets[offsets.length - 1]).toBe("UTC+14:00");
      expect(offsets).toContain("UTC+05:45");
      await userEvent.selectOptions(offsetSelect, "UTC+02:00");

      fireEvent.change(within(dialog).getByLabelText("First day (optional)"), { target: { value: "2018-07-01" } });
      await userEvent.type(within(dialog).getByLabelText("Note (optional)"), "  Summer time all year  ");
      await userEvent.click(submit);

      expect(mockApi.createCameraClockRule).toHaveBeenCalledTimes(1);
      expect(mockApi.createCameraClockRule).toHaveBeenCalledWith({
        make: "Panasonic",
        model: "DC-TZ200",
        utc_offset_min: 120,
        starts_on: "2018-07-01",
        ends_on: null,
        note: "Summer time all year",
      });
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(mockApi.listCameraClockRules).toHaveBeenCalledTimes(2);
    });

    it("creates one for a camera typed by hand", async () => {
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), "Other camera");
      expect(within(dialog).getByText(/EXIF Make and Model/)).toBeInTheDocument();
      await userEvent.type(within(dialog).getByLabelText("Make"), "  Arashi Vision ");
      await userEvent.type(within(dialog).getByLabelText("Model"), "Insta360 ONE RS");
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+01:00");
      fireEvent.change(within(dialog).getByLabelText("First day (optional)"), { target: { value: "2022-06-01" } });
      fireEvent.change(within(dialog).getByLabelText("Last day (optional)"), { target: { value: "2022-09-30" } });
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));

      expect(mockApi.createCameraClockRule).toHaveBeenCalledWith({
        make: "Arashi Vision",
        model: "Insta360 ONE RS",
        utc_offset_min: 60,
        starts_on: "2022-06-01",
        ends_on: "2022-09-30",
        note: "",
      });
    });

    it("needs both a make and a model when typed", async () => {
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), "Other camera");
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+01:00");
      await userEvent.type(within(dialog).getByLabelText("Make"), "Panasonic");
      await userEvent.type(within(dialog).getByLabelText("Model"), "   ");
      expect(within(dialog).getByRole("button", { name: "Add rule" })).toBeDisabled();
    });

    it("offers only typing when the library lists no cameras", async () => {
      mockApi.listCameras.mockResolvedValue([]);
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      expect(await within(dialog).findByText(/Your library lists no cameras yet/)).toBeInTheDocument();
      expect(within(dialog).getByLabelText("Camera")).toHaveValue("other");
      expect(within(dialog).getByLabelText("Make")).toBeInTheDocument();
    });

    it("offers typing when the cameras cannot be loaded", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listCameras.mockRejectedValue(new Error("boom"));
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      expect(await within(dialog).findByText(/Could not load the cameras in your library/)).toBeInTheDocument();
      expect(within(dialog).getByLabelText("Model")).toBeInTheDocument();
    });

    it("refuses a first day after the last day", async () => {
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      fireEvent.change(within(dialog).getByLabelText("First day (optional)"), { target: { value: "2020-02-01" } });
      fireEvent.change(within(dialog).getByLabelText("Last day (optional)"), { target: { value: "2020-01-31" } });
      expect(within(dialog).getByText("The first day is after the last day.")).toBeInTheDocument();
      expect(within(dialog).getByRole("button", { name: "Add rule" })).toBeDisabled();
    });

    it("says that the camera already has a rule for that period on a 409", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.createCameraClockRule.mockRejectedValue(
        new ApiError("POST /camera-clock-rules failed: 409", 409, "overlaps another rule for this camera")
      );
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      expect(await within(dialog).findByText("This camera already has a rule for part of that period.")).toBeInTheDocument();
      // The dialog stays open with what was entered, ready to try again.
      expect(within(dialog).getByLabelText("Clock showed")).toHaveValue("120");
      expect(within(dialog).getByRole("button", { name: "Add rule" })).toBeEnabled();
    });

    it("says the rule cap was reached on a 409 for it, even when the list on screen is behind", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      const rules = (n: number) => Array.from({ length: n }, (_, i) => makeRule({ id: `r${i}`, model: `Model ${i}` }));
      mockApi.listCameraClockRules.mockResolvedValue(rules(99));
      mockApi.createCameraClockRule.mockRejectedValue(
        new ApiError("POST /camera-clock-rules failed: 409", 409, "at most 100 camera clock rules")
      );
      render(<CameraClockRules />);
      await screen.findByText("Panasonic · Model 98");
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      // A rule added in another tab or by a script is the 100th.
      mockApi.listCameraClockRules.mockResolvedValue(rules(100));
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      expect(await within(dialog).findByText("You already have 100 rules, the most allowed. Delete one first.")).toBeInTheDocument();
      expect(within(dialog).queryByText("This camera already has a rule for part of that period.")).not.toBeInTheDocument();
      // The list catches up, and adding stops.
      expect(await screen.findByText("You have 100 rules, the most allowed. Delete one to add another.")).toBeInTheDocument();
      expect(mockApi.listCameraClockRules).toHaveBeenCalledTimes(2);
    });

    it("shows the server's reason for a 400", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.createCameraClockRule.mockRejectedValue(
        new ApiError("POST /camera-clock-rules failed: 400", 400, "make is longer than 200 characters")
      );
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      expect(await within(dialog).findByText("make is longer than 200 characters")).toBeInTheDocument();
    });

    it("has a fallback for a 400 without a reason, and for other failures", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.createCameraClockRule
        .mockRejectedValueOnce(new ApiError("POST /camera-clock-rules failed: 400", 400))
        .mockRejectedValueOnce(new ApiError("POST /camera-clock-rules failed: 500", 500));
      render(<CameraClockRules />);
      const dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Camera"), TZ200_LABEL);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      expect(await within(dialog).findByText("The server refused this rule.")).toBeInTheDocument();
      await userEvent.click(within(dialog).getByRole("button", { name: "Add rule" }));
      expect(await within(dialog).findByText("Could not save the rule.")).toBeInTheDocument();
    });

    it("starts empty every time it opens", async () => {
      render(<CameraClockRules />);
      let dialog = await openAddDialog();
      await camerasLoaded(dialog);
      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+02:00");
      await userEvent.keyboard("{Escape}");
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

      dialog = await openAddDialog();
      expect(within(dialog).getByLabelText("Clock showed")).toHaveValue("");
      expect(mockApi.createCameraClockRule).not.toHaveBeenCalled();
    });
  });

  describe("editing a rule", () => {
    it("starts from the rule and replaces it", async () => {
      const rule = makeRule({ starts_on: "2018-07-01", note: "Summer time all year" });
      mockApi.listCameraClockRules.mockResolvedValue([rule]);
      render(<CameraClockRules />);
      await userEvent.click(await screen.findByRole("button", { name: "Edit rule for Panasonic · DC-TZ200, from 2018-07-01" }));
      const dialog = await screen.findByRole("dialog");
      expect(within(dialog).getByRole("heading", { name: "Edit clock rule" })).toBeInTheDocument();
      await camerasLoaded(dialog);

      // The rule's camera is the library's, matched without case.
      const cameraSelect = within(dialog).getByLabelText("Camera") as HTMLSelectElement;
      expect(cameraSelect.selectedOptions[0].textContent).toBe(TZ200_LABEL);
      expect(within(dialog).getByLabelText("Clock showed")).toHaveValue("120");
      expect(within(dialog).getByLabelText("First day (optional)")).toHaveValue("2018-07-01");
      expect(within(dialog).getByLabelText("Last day (optional)")).toHaveValue("");
      expect(within(dialog).getByLabelText("Note (optional)")).toHaveValue("Summer time all year");

      await userEvent.selectOptions(within(dialog).getByLabelText("Clock showed"), "UTC+01:00");
      fireEvent.change(within(dialog).getByLabelText("Last day (optional)"), { target: { value: "2019-03-31" } });
      await userEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

      expect(mockApi.updateCameraClockRule).toHaveBeenCalledWith("r1", {
        make: "Panasonic",
        model: "DC-TZ200",
        utc_offset_min: 60,
        starts_on: "2018-07-01",
        ends_on: "2019-03-31",
        note: "Summer time all year",
      });
      expect(mockApi.createCameraClockRule).not.toHaveBeenCalled();
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
      expect(mockApi.listCameraClockRules).toHaveBeenCalledTimes(2);
    });

    it("keeps a camera the library does not list as typed", async () => {
      mockApi.listCameraClockRules.mockResolvedValue([makeRule({ make: "Arashi Vision", model: "Insta360 ONE RS", utc_offset_min: 60 })]);
      render(<CameraClockRules />);
      await userEvent.click(await screen.findByRole("button", { name: "Edit rule for Arashi Vision · Insta360 ONE RS, always" }));
      const dialog = await screen.findByRole("dialog");
      await camerasLoaded(dialog);
      expect(within(dialog).getByLabelText("Camera")).toHaveValue("other");
      expect(within(dialog).getByLabelText("Make")).toHaveValue("Arashi Vision");
      expect(within(dialog).getByLabelText("Model")).toHaveValue("Insta360 ONE RS");
      await userEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
      expect(mockApi.updateCameraClockRule).toHaveBeenCalledWith("r1", expect.objectContaining({ make: "Arashi Vision", model: "Insta360 ONE RS" }));
    });

    it("says that the camera already has a rule for that period on a 409", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      mockApi.updateCameraClockRule.mockRejectedValue(new ApiError("PUT /camera-clock-rules/r1 failed: 409", 409));
      render(<CameraClockRules />);
      await userEvent.click(await screen.findByRole("button", { name: /^Edit rule for Panasonic · DC-TZ200/ }));
      const dialog = await screen.findByRole("dialog");
      await camerasLoaded(dialog);
      await userEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
      expect(await within(dialog).findByText("This camera already has a rule for part of that period.")).toBeInTheDocument();
    });

    it("says when the rule was deleted in the meantime, and refreshes the list", async () => {
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      mockApi.updateCameraClockRule.mockRejectedValue(new ApiError("PUT /camera-clock-rules/r1 failed: 404", 404));
      render(<CameraClockRules />);
      await userEvent.click(await screen.findByRole("button", { name: /^Edit rule for Panasonic · DC-TZ200/ }));
      const dialog = await screen.findByRole("dialog");
      await camerasLoaded(dialog);
      mockApi.listCameraClockRules.mockResolvedValue([]);
      await userEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
      expect(await within(dialog).findByText("This rule was deleted in the meantime.")).toBeInTheDocument();
      expect(mockApi.listCameraClockRules).toHaveBeenCalledTimes(2);
    });
  });

  describe("deleting a rule", () => {
    it("deletes only after confirmation, then refreshes", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      render(<CameraClockRules />);
      const button = await screen.findByRole("button", { name: "Delete rule for Panasonic · DC-TZ200, always" });

      await userEvent.click(button);
      expect(confirmSpy).toHaveBeenLastCalledWith(
        "Delete the clock rule for Panasonic · DC-TZ200 (always)? New uploads from this camera will be read without it. Items already in your library keep their times."
      );
      expect(mockApi.deleteCameraClockRule).not.toHaveBeenCalled();

      mockApi.listCameraClockRules.mockResolvedValue([]);
      await userEvent.click(button);
      expect(mockApi.deleteCameraClockRule).toHaveBeenCalledWith("r1");
      expect(await screen.findByText("No clock rules yet.")).toBeInTheDocument();
    });

    it("says when a delete fails", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      vi.spyOn(console, "error").mockImplementation(() => {});
      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      mockApi.deleteCameraClockRule.mockRejectedValue(new ApiError("DELETE /camera-clock-rules/r1 failed: 500", 500));
      render(<CameraClockRules />);
      await userEvent.click(await screen.findByRole("button", { name: /^Delete rule for/ }));
      expect(await screen.findByText("Could not delete the rule")).toBeInTheDocument();
    });

    it("treats a rule already deleted elsewhere as deleted", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      mockApi.listCameraClockRules.mockResolvedValue([makeRule()]);
      mockApi.deleteCameraClockRule.mockRejectedValue(new ApiError("DELETE /camera-clock-rules/r1 failed: 404", 404));
      render(<CameraClockRules />);
      const button = await screen.findByRole("button", { name: /^Delete rule for/ });
      mockApi.listCameraClockRules.mockResolvedValue([]);
      await userEvent.click(button);
      expect(await screen.findByText("No clock rules yet.")).toBeInTheDocument();
      expect(screen.queryByText("Could not delete the rule")).not.toBeInTheDocument();
    });
  });
});
