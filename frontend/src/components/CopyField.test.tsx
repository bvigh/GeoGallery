import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi, afterEach } from "vitest";
import { CopyField } from "@/components/CopyField";

describe("CopyField", () => {
  const originalClipboard = Object.getOwnPropertyDescriptor(navigator, "clipboard");

  afterEach(() => {
    if (originalClipboard) Object.defineProperty(navigator, "clipboard", originalClipboard);
    else delete (navigator as { clipboard?: unknown }).clipboard;
  });

  it("copies the value", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<CopyField value="https://example.test/invite" label="Invite link" />);
    await userEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith("https://example.test/invite");
    expect(await screen.findByText("Copied")).toBeInTheDocument();
  });

  it("has a finger-sized button and a field iOS does not zoom into", () => {
    render(<CopyField value="gg_pat_x" label="New API token" />);
    expect(screen.getByRole("button", { name: "Copy" })).toHaveClass("touch:min-h-11");
    expect(screen.getByLabelText("New API token")).toHaveClass("touch:text-base");
  });
});
