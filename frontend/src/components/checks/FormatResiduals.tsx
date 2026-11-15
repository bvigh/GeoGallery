import { useId } from "react";
import { formatDistance, NO_RESIDUALS, RESIDUALS_CAPTION } from "@/lib/checks";
import { formatCount } from "@/lib/numbers";
import type { ChecksResidual } from "@/types";

const cell = "py-1 pr-3 align-top";

// Each log format against the photos that carry their own position: the most informative table the
// design found, so it is shown whole.
export function FormatResiduals({ residuals }: { residuals: ChecksResidual[] }) {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="space-y-2">
      <h3 id={headingId} className="text-sm font-semibold">
        Formats against your photos
      </h3>
      {residuals.length === 0 ? (
        <p className="text-sm text-muted-foreground">{NO_RESIDUALS}</p>
      ) : (
        <table className="w-full max-w-xl text-left text-xs">
          <caption className="caption-bottom pt-1 text-left text-muted-foreground">{RESIDUALS_CAPTION}</caption>
          <thead className="text-muted-foreground">
            <tr>
              <th scope="col" className={`${cell} font-normal`}>
                Format
              </th>
              <th scope="col" className={`${cell} text-right font-normal`}>
                Photos
              </th>
              <th scope="col" className={`${cell} text-right font-normal`}>
                Median
              </th>
              <th scope="col" className={`${cell} text-right font-normal`}>
                90th percentile
              </th>
            </tr>
          </thead>
          <tbody>
            {residuals.map((residual) => (
              <tr key={residual.format} className="border-t border-border">
                <td className={`${cell} break-all`}>{residual.format}</td>
                <td className={`${cell} text-right tabular-nums`}>{formatCount(residual.n)}</td>
                <td className={`${cell} text-right tabular-nums`}>{formatDistance(residual.p50_m)}</td>
                <td className={`${cell} text-right tabular-nums`}>{formatDistance(residual.p90_m)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
