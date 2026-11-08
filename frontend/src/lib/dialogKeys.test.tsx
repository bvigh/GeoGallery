import { useState } from "react";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect } from "vitest";
import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";
import { keyBelongsToDialog } from "@/lib/dialogKeys";

const key = (init: KeyboardEventInit = { key: "ArrowRight" }) => new KeyboardEvent("keydown", { cancelable: true, ...init });

function Host() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open</button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent>
          <DialogTitle>Albums</DialogTitle>
          <DialogDescription>Pick some</DialogDescription>
        </DialogContent>
      </Dialog>
    </>
  );
}

describe("keyBelongsToDialog", () => {
  it("claims keys only while a dialog is open", async () => {
    render(<Host />);
    expect(keyBelongsToDialog(key())).toBe(false);

    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(keyBelongsToDialog(key())).toBe(true);

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(keyBelongsToDialog(key())).toBe(false);
  });

  // In a browser the dialog's close commits before the event reaches window, so a view behind it
  // finds no open dialog: only the handled mark tells it the Escape was the dialog's.
  it("marks the Escape that closed a dialog as handled, whatever has focus", async () => {
    render(<Host />);
    await userEvent.click(screen.getByRole("button", { name: "Open" }));
    act(() => (document.activeElement as HTMLElement).blur());
    expect(document.activeElement).toBe(document.body);

    const handled: boolean[] = [];
    const onWindowKey = (e: KeyboardEvent) => handled.push(e.defaultPrevented);
    window.addEventListener("keydown", onWindowKey);
    try {
      await userEvent.keyboard("{Escape}");
    } finally {
      window.removeEventListener("keydown", onWindowKey);
    }
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(handled).toEqual([true]);
  });

  it("claims a key something else already handled", () => {
    const e = key({ key: "Escape" });
    e.preventDefault();
    expect(keyBelongsToDialog(e)).toBe(true);
  });
});
