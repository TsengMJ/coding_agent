import type { Message } from "../types";

export function textOf(m: Message | undefined): string {
  if (!m?.content) return "";
  return m.content
    .filter((b) => b.type === "text")
    .map((b) => b.text ?? "")
    .join("");
}

export function reasoningOf(m: Message): string {
  if (!m.content) return "";
  return m.content
    .filter((b) => b.type === "reasoning")
    .map((b) => b.text ?? "")
    .join("");
}

export interface Timing {
  start: number;
  durationMs: number;
}

export function timingOf(m: Message | undefined): Timing | undefined {
  const md = m?.metadata;
  if (!md) return undefined;
  const start = Number(md["started_at_ms"]);
  const dur = Number(md["duration_ms"]);
  if (!Number.isFinite(start) || !Number.isFinite(dur) || md["started_at_ms"] === undefined) return undefined;
  return { start, durationMs: dur };
}

export function traceSeqOf(m: Message): number | undefined {
  const v = m.metadata?.["trace_seq"];
  return typeof v === "number" ? v : undefined;
}

export function nsKey(ns: string[] | undefined): string {
  return (ns ?? []).join("/");
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.floor((ms % 60_000) / 1000);
  return `${m}m${String(s).padStart(2, "0")}s`;
}

export function formatTokens(n: number): string {
  return n < 1000 ? String(n) : `${(n / 1000).toFixed(1)}k`;
}

export function str(v: unknown): string {
  if (typeof v === "string") return v;
  if (v === undefined || v === null) return "";
  return JSON.stringify(v);
}
