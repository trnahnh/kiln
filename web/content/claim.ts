// The one number under the headline, on the README and on the hero. Both render this
// string, and scripts/pull-metrics.ts --check fails if the README's copy drifts from it.
export interface ProvisioningRow {
  p50: string;
  p95: string;
  n: number;
  baseline: string;
}

export function provisioningClaim(row: ProvisioningRow): string {
  const wait = row.baseline.match(/hours to a day/);
  if (!wait) throw new Error(`provisioning baseline no longer says "hours to a day": ${row.baseline}`);
  return `Provisioning a standard Postgres: ${row.p50} p50, ${row.p95} p95, n=${row.n}. The status quo, a Terraform pull request with human review, waits ${wait[0]}.`;
}

export const claimMarker = { open: "<!-- claim:provisioning -->", close: "<!-- /claim:provisioning -->" };
