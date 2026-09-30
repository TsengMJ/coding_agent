import type { SlashCommand } from "../types";

// matchCommands lists commands for the autocomplete popup: shown only while
// the input is a single "/word" (no arguments yet), matched by name or alias
// prefix.
export function matchCommands(input: string, cmds: SlashCommand[]): SlashCommand[] {
  if (!input.startsWith("/") || /\s/.test(input)) return [];
  return cmds.filter((c) => [c.name, ...(c.aliases ?? [])].some((n) => n.startsWith(input)));
}

export type Resolved =
  | { kind: "command"; name: string; args: string }
  | { kind: "unknown"; name: string; suggestion?: string }
  | { kind: "text" };

// resolveCommand maps "/name args" to the canonical command name, following
// aliases. Plain text resolves to "text".
export function resolveCommand(input: string, cmds: SlashCommand[]): Resolved {
  const text = input.trim();
  if (!text.startsWith("/")) return { kind: "text" };
  const space = text.search(/\s/);
  const name = space < 0 ? text : text.slice(0, space);
  const args = space < 0 ? "" : text.slice(space).trim();
  const cmd = cmds.find((c) => c.name === name || (c.aliases ?? []).includes(name));
  if (cmd) return { kind: "command", name: cmd.name, args };
  return { kind: "unknown", name, suggestion: suggest(name, cmds) };
}

function suggest(name: string, cmds: SlashCommand[]): string | undefined {
  const names = cmds.flatMap((c) => [c.name, ...(c.aliases ?? [])]).sort();
  const prefixed = names.filter((n) => n.startsWith(name));
  if (prefixed.length === 1) return prefixed[0];
  let best: string | undefined;
  let bestDist = 3;
  for (const n of names) {
    const d = editDistance(name, n);
    if (d < bestDist) {
      best = n;
      bestDist = d;
    }
  }
  return best;
}

function editDistance(a: string, b: string): number {
  let prev = Array.from({ length: b.length + 1 }, (_, j) => j);
  for (let i = 1; i <= a.length; i++) {
    const cur = [i];
    for (let j = 1; j <= b.length; j++) {
      cur[j] = Math.min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (a[i - 1] === b[j - 1] ? 0 : 1));
    }
    prev = cur;
  }
  return prev[b.length];
}

export function usage(c: SlashCommand): string {
  return c.args ? `${c.name} ${c.args}` : c.name;
}

// helpText lists commands one per line, like the TUI's /help.
export function helpText(cmds: SlashCommand[]): string {
  return cmds
    .map((c) => `${usage(c)} — ${c.description ?? ""}${c.aliases?.length ? ` (${c.aliases.join(", ")})` : ""}`)
    .join("\n");
}
