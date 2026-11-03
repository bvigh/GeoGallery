import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import VideoDecodeNotice from "./VideoDecodeNotice";

describe("VideoDecodeNotice", () => {
  it("links to the playback guide in a new tab", () => {
    render(<VideoDecodeNotice />);

    expect(screen.getByRole("alert")).toHaveTextContent("HEVC");
    const link = screen.getByRole("link", { name: "How to enable HEVC playback" });
    expect(link).toHaveAttribute("href", "/video-playback.html");
    expect(link).toHaveAttribute("target", "_blank");
  });

  it("keeps clicks away from the viewer underneath", async () => {
    const onViewerClick = vi.fn();
    render(
      <div onClick={onViewerClick}>
        <VideoDecodeNotice />
      </div>
    );

    await userEvent.click(screen.getByRole("alert"));
    expect(onViewerClick).not.toHaveBeenCalled();
  });
});
