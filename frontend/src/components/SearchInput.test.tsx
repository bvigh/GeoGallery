import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, it, expect, vi } from "vitest";
import { SearchInput } from "@/components/SearchInput";

function Harness({ onSubmit }: { onSubmit: () => void }) {
  const [value, setValue] = useState("");
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <SearchInput value={value} onChange={setValue} label="Search albums" />
    </form>
  );
}

describe("SearchInput", () => {
  it("filters as you type and never submits the form on Enter", async () => {
    const onSubmit = vi.fn();
    render(<Harness onSubmit={onSubmit} />);
    const input = screen.getByRole("searchbox", { name: "Search albums" });
    await userEvent.type(input, "ice{Enter}");
    expect(input).toHaveValue("ice");
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("offers a clear button only while there is a query", async () => {
    render(<Harness onSubmit={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Clear search" })).not.toBeInTheDocument();
    const input = screen.getByRole("searchbox", { name: "Search albums" });
    await userEvent.type(input, "ice");
    await userEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(input).toHaveValue("");
    expect(screen.queryByRole("button", { name: "Clear search" })).not.toBeInTheDocument();
  });

  it("is finger-sized on touch, with text large enough that iOS does not zoom in", async () => {
    render(<Harness onSubmit={vi.fn()} />);
    const input = screen.getByRole("searchbox", { name: "Search albums" });
    expect(input).toHaveClass("touch:text-base", "touch:pr-11");
    await userEvent.type(input, "ice");
    expect(screen.getByRole("button", { name: "Clear search" })).toHaveClass("touch:h-11", "touch:w-11");
  });
});
