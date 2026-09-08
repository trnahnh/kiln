"use client";

import { useEffect, useMemo, useState } from "react";

interface Entry {
  seq: number;
  eventId: string;
  actor: string;
  action: string;
  resource: string;
  occurredAt: string;
  details: Record<string, string | number>;
}

// Five entries in the wire format, in the shape of the validation week. The hashes are
// computed in the browser with the rule from docs/DATA_MODEL.md, so what breaks here is
// the same thing GET /v1/audit/verify would report.
const BASE: Entry[] = [
  {
    seq: 1,
    eventId: "2b1f0e0c-52aa-5c3e-9a71-1e8d4a0b9c01",
    actor: "dev03@kiln.sim",
    action: "PROVISION_REQUEST",
    resource: "DatabaseClaim/dev03/dev03-db",
    occurredAt: "2026-09-07T14:02:00.104512Z",
    details: { outcome: "Received", kind: "DatabaseClaim" },
  },
  {
    seq: 2,
    eventId: "6f1c2c1e-7d1e-5d0b-9a8e-3c1b7a4f2e10",
    actor: "system:operator",
    action: "PROVISION",
    resource: "TenantDatabase/dev03/dev03-db",
    occurredAt: "2026-09-07T14:02:11.418213Z",
    details: { outcome: "Ready" },
  },
  {
    seq: 3,
    eventId: "9c4d7a12-0b3e-5f61-8d2a-7e5c1b9f3a44",
    actor: "system:kiln-scheduler",
    action: "SCHEDULE",
    resource: "Pod/dev03/dev03-db-0",
    occurredAt: "2026-09-07T14:02:03.771004Z",
    details: { outcome: "Bound", node: "kind-worker2", workloadClass: "latency-sensitive" },
  },
  {
    seq: 4,
    eventId: "d3e8b5f0-4a2c-5e97-b1d6-0f7a2c8e5b19",
    actor: "dev03@kiln.sim",
    action: "DEPLOY",
    resource: "CanaryRollout/dev03/dev03-svc",
    occurredAt: "2026-09-07T14:06:30.220871Z",
    details: { outcome: "Started", templateHash: "7c9e1a4f" },
  },
  {
    seq: 5,
    eventId: "a71f3c2d-8e5b-5a04-9c6f-2d1e8b7a4f33",
    actor: "system:delivery-controller",
    action: "ROLLBACK",
    resource: "CanaryRollout/dev03/dev03-svc",
    occurredAt: "2026-09-07T14:07:20.502339Z",
    details: { outcome: "RolledBack", criterion: "errorRate", reason: "SPRT accepted regression", templateHash: "7c9e1a4f" },
  },
];

const ZERO = "0".repeat(64);

function canonical(value: unknown): string {
  if (Array.isArray(value)) return "[" + value.map(canonical).join(",") + "]";
  if (value && typeof value === "object") {
    const o = value as Record<string, unknown>;
    return "{" + Object.keys(o).sort().map((k) => JSON.stringify(k) + ":" + canonical(o[k])).join(",") + "}";
  }
  return JSON.stringify(value);
}

async function sha256(text: string): Promise<string> {
  const buf = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, "0")).join("");
}

function hashOf(prev: string, e: Entry): Promise<string> {
  return sha256([prev, e.eventId, e.actor, e.action, e.resource, e.occurredAt, canonical(e.details)].join("\n"));
}

interface Edit {
  actor?: string;
  outcome?: string;
  rehash?: boolean;
}

interface RowResult {
  storedPrev: string;
  stored: string;
  recomputed: string;
  reasons: string[];
}

export default function HashChainDemo() {
  const [edits, setEdits] = useState<Record<number, Edit>>({});
  const [original, setOriginal] = useState<string[] | null>(null);
  const [rows, setRows] = useState<RowResult[] | null>(null);

  const entries = useMemo(
    () =>
      BASE.map((e) => {
        const ed = edits[e.seq];
        if (!ed) return e;
        return {
          ...e,
          actor: ed.actor ?? e.actor,
          details: ed.outcome !== undefined ? { ...e.details, outcome: ed.outcome } : e.details,
        };
      }),
    [edits],
  );

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const chain: string[] = [];
      let prev = ZERO;
      for (const e of BASE) {
        prev = await hashOf(prev, e);
        chain.push(prev);
      }
      if (!cancelled) setOriginal(chain);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!original) return;
    let cancelled = false;
    (async () => {
      const out: RowResult[] = [];
      const stored: string[] = [];
      for (let i = 0; i < entries.length; i++) {
        const storedPrev = i === 0 ? ZERO : original[i - 1];
        const recomputed = await hashOf(storedPrev, entries[i]);
        const storedHash = edits[entries[i].seq]?.rehash ? recomputed : original[i];
        stored.push(storedHash);
        const reasons: string[] = [];
        if (recomputed !== storedHash) reasons.push("hash mismatch");
        if (i > 0 && storedPrev !== stored[i - 1]) reasons.push("prevHash mismatch");
        out.push({ storedPrev, stored: storedHash, recomputed, reasons });
      }
      if (!cancelled) setRows(out);
    })();
    return () => {
      cancelled = true;
    };
  }, [entries, edits, original]);

  const broken = rows?.filter((r) => r.reasons.length) ?? [];
  const verify =
    rows === null
      ? "computing…"
      : broken.length === 0
        ? JSON.stringify({ ok: true, entries: rows.length })
        : JSON.stringify(
            {
              ok: false,
              code: "AUDIT_CHAIN_BROKEN",
              brokenLinks: rows
                .map((r, i) => ({ seq: BASE[i].seq, eventId: BASE[i].eventId, reason: r.reasons.join(", ") }))
                .filter((_, i) => rows[i].reasons.length),
            },
            null,
            2,
          );

  const setEdit = (seq: number, patch: Edit) => setEdits((cur) => ({ ...cur, [seq]: { ...cur[seq], ...patch } }));
  const dirty = Object.keys(edits).length > 0;

  return (
    <div className="demo">
      <div className="flex flex-wrap items-baseline justify-between gap-3 border-b border-hairline px-4 py-3">
        <p className="text-[13px] text-fg-muted">Edit any actor or outcome. Every hash is recomputed here, with the service&apos;s rule.</p>
        <button type="button" className="text-[13px] text-fg-faint underline underline-offset-4 hover:text-fg-muted disabled:opacity-40" disabled={!dirty} onClick={() => setEdits({})}>
          Restore all five
        </button>
      </div>
      <ol className="divide-y divide-hairline">
        {entries.map((e, i) => {
          const r = rows?.[i];
          const bad = (r?.reasons.length ?? 0) > 0;
          const ed = edits[e.seq] ?? {};
          return (
            <li key={e.seq} className={"chain-row" + (bad ? " chain-row-bad" : "")}>
              <div className="chain-meta">
                <span className="text-fg-faint tabular-nums">seq {e.seq}</span>
                <span className="text-fg">{e.action}</span>
                <span className="truncate text-fg-muted">{e.resource}</span>
              </div>
              <div className="chain-fields">
                <label className="chain-field">
                  <span>actor</span>
                  <input value={e.actor} onChange={(ev) => setEdit(e.seq, { actor: ev.target.value })} spellCheck={false} />
                </label>
                <label className="chain-field">
                  <span>details.outcome</span>
                  <input value={String(e.details.outcome)} onChange={(ev) => setEdit(e.seq, { outcome: ev.target.value })} spellCheck={false} />
                </label>
                <label className="chain-toggle">
                  <input type="checkbox" checked={!!ed.rehash} onChange={(ev) => setEdit(e.seq, { rehash: ev.target.checked })} />
                  <span>also rewrite this row&apos;s stored hash</span>
                </label>
              </div>
              <div className="chain-hashes">
                <span className="text-fg-faint">prev</span>
                <code>{r ? r.storedPrev.slice(0, 16) : "…"}</code>
                <span className="text-fg-faint">stored</span>
                <code>{r ? r.stored.slice(0, 16) : "…"}</code>
                <span className="text-fg-faint">recomputed</span>
                <code className={bad ? "text-alarm" : ""}>{r ? r.recomputed.slice(0, 16) : "…"}</code>
                {bad && <span className="chain-reason">{r?.reasons.join(", ")}</span>}
              </div>
            </li>
          );
        })}
      </ol>
      <div className="border-t border-hairline px-4 py-3">
        <p className="text-[12px] text-fg-faint">GET /v1/audit/verify</p>
        <pre className={"code mt-1 whitespace-pre-wrap " + (broken.length ? "text-alarm" : "text-facet-highlight")}>{verify}</pre>
      </div>
    </div>
  );
}
