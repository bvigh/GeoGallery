// @vitest-environment node
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import postcss from "postcss";
import tailwindcss from "tailwindcss";
import { describe, expect, it } from "vitest";
import config from "./tailwind.config.js";

const indexCss = fileURLToPath(new URL("./src/index.css", import.meta.url));

// Builds src/index.css with the project config, scanning only the given classes.
async function build(classes: string): Promise<string> {
  const result = await postcss([tailwindcss({ ...config, content: [{ raw: classes }] })]).process(
    readFileSync(indexCss, "utf8"),
    { from: indexCss }
  );
  return result.css;
}

// The position of a class's rule in the stylesheet; a later rule wins over an earlier one.
function position(css: string, selector: string): number {
  const at = css.indexOf(`${selector} {`);
  expect(at, `${selector} is generated`).toBeGreaterThanOrEqual(0);
  return at;
}

describe("tailwind variants", () => {
  it("emits short: after sm:, md: and lg:, so a sideways phone's override wins", async () => {
    const css = await build("sm:p-2 md:p-4 lg:p-6 short:p-1");
    expect(css).toContain(`@media ${config.theme.extend.screens.short.raw} {`);
    const short = position(css, ".short\\:p-1");
    for (const sel of [".sm\\:p-2", ".md\\:p-4", ".lg\\:p-6"]) {
      expect(position(css, sel)).toBeLessThan(short);
    }
  });

  it("emits touch: and mouse: as pointer media queries, before the screens", async () => {
    const css = await build("touch:min-w-10 sm:touch:min-w-11 mouse:hidden");
    // [^@]*: Tailwind merges the rule into index.css's own (pointer: coarse) block when adjacent.
    expect(css).toMatch(/@media \(pointer: coarse\) \{[^@]*\.touch\\:min-w-10 \{/);
    expect(css).toMatch(/@media \(hover: hover\) and \(pointer: fine\) \{\s*\.mouse\\:hidden \{/);
    // The header's tabs are 40px wide on phones and 44px from sm up.
    expect(position(css, ".touch\\:min-w-10")).toBeLessThan(position(css, ".sm\\:touch\\:min-w-11"));
  });

  // The cost of the raw short screen, documented in tailwind.config.js. If these start producing
  // CSS, the comment there (and CLAUDE.md) is out of date.
  it("generates no max-* or arbitrary min-[…] screen variants", async () => {
    const css = await build("max-sm:hidden max-md:flex min-[400px]:block sm:max-lg:hidden");
    expect(css).not.toMatch(/max-sm|max-md|min-\\\[400px\\\]|max-lg/);
  });
});

describe("Leaflet controls on touch", () => {
  // The rules in index.css that size every map's zoom buttons and the popup's close button for a
  // finger. Only under (pointer: coarse): a mouse keeps Leaflet's sizes.
  it("makes the zoom and popup close buttons 44 px only for a coarse pointer", async () => {
    const css = await build("");
    const coarse = [...css.matchAll(/@media \(pointer: coarse\) \{([\s\S]*?)\n\}/g)].map((m) => m[1]).join("\n");
    for (const button of ["a.leaflet-control-zoom-in", "a.leaflet-control-zoom-out", "a.leaflet-popup-close-button"]) {
      const at = coarse.indexOf(button);
      expect(at, `${button} is sized under (pointer: coarse)`).toBeGreaterThanOrEqual(0);
      const rule = coarse.slice(at, coarse.indexOf("}", at));
      expect(rule).toMatch(/width: 44px;[\s\S]*height: 44px;/);
    }
    expect(coarse).toMatch(/\.leaflet-popup-content \{\s*margin-right: 44px;/);
    // Outside the media query, nothing overrides Leaflet.
    const outside = css.replace(/@media \(pointer: coarse\) \{[\s\S]*?\n\}/g, "");
    expect(outside).not.toContain("leaflet-control-zoom-in");
    expect(outside).not.toContain("leaflet-popup-close-button");
  });
});

describe("overflow-clip-safe", () => {
  // Browsers without overflow: clip (iOS Safari < 16) drop the second declaration and keep hidden.
  it("declares overflow: hidden before overflow: clip", async () => {
    const css = await build("overflow-clip-safe");
    expect(css).toMatch(/\.overflow-clip-safe \{\s*overflow: hidden;\s*overflow: clip;?\s*\}/);
  });
});
