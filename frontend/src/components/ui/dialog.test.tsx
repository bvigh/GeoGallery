import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

function renderDialog(onOpenChange = vi.fn(), className?: string) {
  render(
    <Dialog open onOpenChange={onOpenChange}>
      <DialogContent className={className}>
        <DialogHeader>
          <DialogTitle>A title long enough to reach the close button</DialogTitle>
          <DialogDescription>Description</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <button>Done</button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
  return screen.getByRole("dialog");
}

// jsdom does no layout, so these pin the classes that make a dialog fit a phone.
describe("DialogContent", () => {
  it("stays inside a phone's screen and scrolls its own content", () => {
    const dialog = renderDialog();
    expect(dialog).toHaveClass("w-[calc(100%-2rem)]", "overflow-y-auto");
    // A vh cap first: without dvh support the dvh class alone would leave no height cap.
    expect(dialog).toHaveClass("max-h-[calc(100vh-2rem)]", "supports-[height:100dvh]:max-h-[calc(100dvh-2rem)]");
    // At the top on phones, centred from sm up.
    expect(dialog).toHaveClass("top-4", "sm:top-[50%]", "sm:translate-y-[-50%]");
  });

  it("lets the caller narrow it", () => {
    const dialog = renderDialog(vi.fn(), "max-w-sm");
    expect(dialog).toHaveClass("max-w-sm");
    expect(dialog).not.toHaveClass("max-w-lg");
  });

  it("has a named close button that is finger-sized on touch", async () => {
    const onOpenChange = vi.fn();
    const dialog = renderDialog(onOpenChange);
    const close = within(dialog).getByRole("button", { name: "Close" });
    expect(close).toHaveClass("touch:h-11", "touch:w-11");
    await userEvent.click(close);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });

  it("keeps the title clear of the close button and makes footer buttons finger-sized", () => {
    const dialog = renderDialog();
    const header = within(dialog).getByText("A title long enough to reach the close button").parentElement!;
    expect(header).toHaveClass("pr-6", "touch:pr-8");
    expect(within(dialog).getByRole("button", { name: "Done" }).parentElement).toHaveClass("touch:[&>*]:min-h-11");
  });
});
