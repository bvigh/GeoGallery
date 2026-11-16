import { afterEach, describe, expect, it, vi } from "vitest";
import { isTextField, keepTabInside } from "@/lib/focusTrap";

// A view (tabindex -1, as the fullscreen views are) with the given markup inside, and a button
// behind it on the page.
function setup(inner: string) {
  const behind = document.createElement("button");
  behind.textContent = "behind";
  const root = document.createElement("div");
  root.tabIndex = -1;
  root.innerHTML = inner;
  document.body.append(behind, root);
  return { root, behind };
}

function trap(root: HTMLElement, shiftKey = false): KeyboardEvent {
  const e = new KeyboardEvent("keydown", { key: "Tab", shiftKey, cancelable: true });
  keepTabInside(e, root);
  return e;
}

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("keepTabInside", () => {
  it("wraps Tab from the last control to the first, and Shift+Tab from the first to the last", () => {
    const { root } = setup('<button id="a">a</button><a id="b" href="/x">b</a><input id="c" />');
    const [a, c] = [root.querySelector<HTMLElement>("#a")!, root.querySelector<HTMLElement>("#c")!];

    c.focus();
    const forward = trap(root);
    expect(forward.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(a);

    const back = trap(root, true);
    expect(back.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(c);
  });

  it("leaves a Tab between two controls inside to the browser", () => {
    const { root } = setup('<button id="a">a</button><button id="b">b</button>');
    root.querySelector<HTMLElement>("#a")!.focus();
    expect(trap(root).defaultPrevented).toBe(false);
  });

  it("brings the focus in from the page behind, and Shift+Tab from the view itself to the last control", () => {
    const { root, behind } = setup('<button id="a">a</button><button id="b">b</button>');
    behind.focus();
    expect(trap(root).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(root.querySelector("#a"));

    root.focus();
    trap(root, true);
    expect(document.activeElement).toBe(root.querySelector("#b"));
  });

  it("skips disabled controls and elements taken out of the tab order", () => {
    const { root } = setup(
      '<button id="a">a</button><button id="off" disabled>off</button><div tabindex="-1" id="out"></div>',
    );
    const a = root.querySelector<HTMLElement>("#a")!;
    a.focus();
    trap(root);
    expect(document.activeElement).toBe(a);
  });

  it("counts a video with controls, whose timeline a keyboard user seeks on", () => {
    const { root } = setup('<button id="a">a</button><video controls></video><video></video>');
    const [withControls, without] = Array.from(root.querySelectorAll("video"));
    // jsdom cannot focus a video; a browser can.
    const focusWith = vi.spyOn(withControls, "focus");
    const focusWithout = vi.spyOn(without, "focus");
    root.querySelector<HTMLElement>("#a")!.focus();
    trap(root, true);
    expect(focusWith).toHaveBeenCalledTimes(1);
    expect(focusWithout).not.toHaveBeenCalled();
  });

  it("keeps the focus where it is in a view with nothing to focus", () => {
    const { root } = setup("<p>nothing</p>");
    root.focus();
    expect(trap(root).defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(root);
  });

  it("does nothing without a view", () => {
    const { behind } = setup("");
    behind.focus();
    const e = new KeyboardEvent("keydown", { key: "Tab", cancelable: true });
    keepTabInside(e, null);
    expect(e.defaultPrevented).toBe(false);
    expect(document.activeElement).toBe(behind);
  });
});

describe("isTextField", () => {
  it.each([
    ["an input", "<input />", true],
    ["a range input", '<input type="range" />', true],
    ["a text area", "<textarea></textarea>", true],
    ["a select", "<select></select>", true],
    ["an editable element", '<div contenteditable="true"></div>', true],
    ["a button", "<button>b</button>", false],
    ["a video", "<video controls></video>", false],
  ])("%s", (_name, markup, expected) => {
    const { root } = setup(markup);
    const element = root.firstElementChild!;
    // jsdom does not compute isContentEditable.
    if (element.hasAttribute("contenteditable")) Object.defineProperty(element, "isContentEditable", { value: true });
    expect(isTextField(element)).toBe(expected);
  });

  it("is false without a target", () => {
    expect(isTextField(null)).toBe(false);
  });
});
