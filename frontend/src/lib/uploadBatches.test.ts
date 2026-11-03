import { describe, it, expect } from "vitest";
import { planUploadBatches } from "@/lib/uploadBatches";

function file(name: string, size: number): File {
  const f = new File([], name);
  Object.defineProperty(f, "size", { value: size });
  return f;
}

const names = (batches: File[][]) => batches.map((b) => b.map((f) => f.name));

describe("planUploadBatches", () => {
  it.each([
    { label: "empty", sizes: [], want: [] },
    { label: "all fit in one batch", sizes: [3, 3, 4], want: [["f0", "f1", "f2"]] },
    { label: "splits when the target is exceeded", sizes: [6, 5, 5], want: [["f0"], ["f1", "f2"]] },
    { label: "file larger than target gets its own batch", sizes: [2, 15, 2], want: [["f0"], ["f1"], ["f2"]] },
  ])("$label", ({ sizes, want }) => {
    const files = sizes.map((s, i) => file(`f${i}`, s));
    const plan = planUploadBatches(files, 10, 100);
    expect(names(plan.batches)).toEqual(want);
    expect(plan.tooLarge).toEqual([]);
  });

  it("caps the number of files per batch", () => {
    const files = [1, 1, 1, 1, 1].map((s, i) => file(`f${i}`, s));
    expect(names(planUploadBatches(files, 10, 100, 2).batches)).toEqual([["f0", "f1"], ["f2", "f3"], ["f4"]]);
  });

  it("separates files over the per-file limit", () => {
    const plan = planUploadBatches([file("ok", 5), file("huge", 101), file("ok2", 5)], 10, 100);
    expect(names(plan.batches)).toEqual([["ok", "ok2"]]);
    expect(plan.tooLarge.map((f) => f.name)).toEqual(["huge"]);
  });
});
