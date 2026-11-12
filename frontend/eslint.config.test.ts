// @vitest-environment node
import { fileURLToPath } from "node:url";
import { ESLint } from "eslint";
import { describe, expect, it } from "vitest";

const cwd = fileURLToPath(new URL(".", import.meta.url));
const eslint = new ESLint({ cwd });

// The restricted properties ESLint reports for this source at this path. The file need not exist.
async function restricted(filePath: string, code: string): Promise<string[]> {
  const [result] = await eslint.lintText(code, { filePath });
  return result.messages.filter((m) => m.ruleId === "no-restricted-properties").map((m) => m.message);
}

describe("no-restricted-properties on evidence surfaces", () => {
  const code = `
    export const a = new Date(0).toLocaleString();
    export const b = new Date(0).toLocaleTimeString();
    export const c = new Date(0).toLocaleDateString();
  `;

  it.each([
    ["src/lib/evidence.ts"],
    ["src/components/evidence/TimeCard.tsx"],
    ["src/components/evidence/nested/Row.ts"],
  ])("refuses browser-local time formatting in %s", async (filePath) => {
    expect(await restricted(filePath, code)).toHaveLength(3);
  });

  it.each([
    ["a test of a component", "src/components/evidence/TimeCard.test.tsx"],
    ["the library's test", "src/lib/evidence.test.ts"],
    ["another page", "src/pages/SettingsPage.tsx"],
    ["another library file", "src/lib/time.ts"],
  ])("leaves %s alone", async (_name, filePath) => {
    expect(await restricted(filePath, code)).toEqual([]);
  });
});
