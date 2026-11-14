import { useRef } from "react";
import { act, render, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readLastViewed, useLastViewed, useRevealLastViewed, writeLastViewed } from "./lastViewed";

function storageThrows() {
  const denied = () => {
    throw new DOMException("The operation is insecure.", "SecurityError");
  };
  vi.spyOn(window, "sessionStorage", "get").mockImplementation(denied);
}

function quotaExceeded() {
  vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
    throw new DOMException("Quota exceeded", "QuotaExceededError");
  });
}

describe("lastViewed", () => {
  beforeEach(() => window.sessionStorage.clear());
  afterEach(() => vi.restoreAllMocks());

  it("keeps one ID per user and page", () => {
    expect(readLastViewed("u1", "trips")).toBeNull();

    writeLastViewed("u1", "trips", "t1");
    writeLastViewed("u1", "albums", "a1");
    writeLastViewed("u2", "trips", "t2");
    writeLastViewed("u1", "trips", "t3");

    expect(readLastViewed("u1", "trips")).toBe("t3");
    expect(readLastViewed("u1", "albums")).toBe("a1");
    expect(readLastViewed("u2", "trips")).toBe("t2");
    expect(readLastViewed("u2", "albums")).toBeNull();
  });

  it("reads nothing and writes nothing when storage is unavailable", () => {
    writeLastViewed("u1", "trips", "t1");
    storageThrows();

    expect(readLastViewed("u1", "trips")).toBeNull();
    expect(() => writeLastViewed("u1", "trips", "t2")).not.toThrow();

    vi.restoreAllMocks();
    expect(readLastViewed("u1", "trips")).toBe("t1");
  });

  it("ignores a write the storage refuses", () => {
    writeLastViewed("u1", "trips", "t1");
    quotaExceeded();

    expect(() => writeLastViewed("u1", "trips", "t2")).not.toThrow();
    expect(readLastViewed("u1", "trips")).toBe("t1");
  });

  describe("useLastViewed", () => {
    it("starts from storage and remembers a new ID there", () => {
      writeLastViewed("u1", "albums", "a1");
      const { result } = renderHook(() => useLastViewed("u1", "albums"));
      expect(result.current[0]).toBe("a1");

      act(() => result.current[1]("a2"));
      expect(result.current[0]).toBe("a2");
      expect(readLastViewed("u1", "albums")).toBe("a2");
    });

    it("never starts from another user's ID", () => {
      writeLastViewed("u2", "albums", "a1");
      const { result } = renderHook(() => useLastViewed("u1", "albums"));
      expect(result.current[0]).toBeNull();
    });

    it("remembers the ID while mounted when storage is unavailable", () => {
      storageThrows();
      const { result, unmount } = renderHook(() => useLastViewed("u1", "trips"));
      expect(result.current[0]).toBeNull();

      act(() => result.current[1]("t1"));
      expect(result.current[0]).toBe("t1");
      unmount();

      expect(renderHook(() => useLastViewed("u1", "trips")).result.current[0]).toBeNull();
    });
  });

  describe("useRevealLastViewed", () => {
    const scrollIntoView = vi.fn();

    beforeEach(() => {
      scrollIntoView.mockClear();
      // jsdom has no scrollIntoView.
      Element.prototype.scrollIntoView = scrollIntoView;
    });

    afterEach(() => {
      delete (Element.prototype as Partial<Element>).scrollIntoView;
    });

    function List({ inSight, marked }: { inSight: boolean; marked: boolean }) {
      const card = useRef<HTMLElement>(null);
      useRevealLastViewed(card, inSight);
      return <article aria-label="card" ref={marked ? card : undefined} />;
    }

    it("scrolls the marked card into view each time the list comes into sight", () => {
      const { getByRole, rerender } = render(<List inSight={false} marked />);
      expect(scrollIntoView).not.toHaveBeenCalled();

      rerender(<List inSight marked />);
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      expect(scrollIntoView).toHaveBeenCalledWith({ block: "nearest" });
      expect(scrollIntoView.mock.contexts[0]).toBe(getByRole("article"));

      // Staying in sight does not scroll again; coming back into sight does.
      rerender(<List inSight marked />);
      expect(scrollIntoView).toHaveBeenCalledTimes(1);
      rerender(<List inSight={false} marked />);
      rerender(<List inSight marked />);
      expect(scrollIntoView).toHaveBeenCalledTimes(2);
    });

    it("does nothing without a marked card", () => {
      render(<List inSight marked={false} />);
      expect(scrollIntoView).not.toHaveBeenCalled();
    });
  });
});
