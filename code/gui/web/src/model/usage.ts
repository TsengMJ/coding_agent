import type { ThreadUsage } from "../types";

// humanTokens formats a token count as 800, 12.3k or 1.2M, like the TUI.
export function humanTokens(n: number): string {
  const trim = (s: string) => s.replace(/\.0$/, "");
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${trim((n / 1000).toFixed(1))}k`;
  return `${trim((n / 1_000_000).toFixed(1))}M`;
}

const usd = (v: number) => `$${v.toFixed(4)}`;

// tokensText is /tokens: context used of the window and model.
export function tokensText(u: ThreadUsage): string {
  if (!u.context_limit) return `${humanTokens(u.context_tokens)} tokens · ${u.model} (context window unknown)`;
  return `${humanTokens(u.context_tokens)} / ${humanTokens(u.context_limit)} tokens (${(u.context_percent ?? 0).toFixed(1)}%) · ${u.model}`;
}

// contextText is /context: what the latest model call's input was made of.
export function contextText(u: ThreadUsage): string {
  const lines = [
    `context ${tokensText(u)}`,
    `  system prompt + tools  ~${humanTokens(u.fixed_tokens)}`,
    `  conversation           ~${humanTokens(u.conversation_tokens)}`,
  ];
  if (u.context_limit) lines.push(`  free                   ${humanTokens(Math.max(u.context_limit - u.context_tokens, 0))}`);
  return lines.join("\n");
}

// costText is /cost: tokens and estimated cost per model, then the total.
export function costText(u: ThreadUsage): string {
  if (!u.models.length) return "no model calls yet";
  const lines = u.models.map((m) => {
    let input = `in ${humanTokens(m.input_tokens)}`;
    if (m.cache_read_tokens) input += ` (${humanTokens(m.cache_read_tokens)} cached)`;
    const calls = m.calls === 1 ? "1 call" : `${m.calls} calls`;
    const price = m.cost_usd === undefined ? "unpriced" : usd(m.cost_usd);
    return `${m.model}  ${calls} · ${input} · out ${humanTokens(m.output_tokens)} · ${price}`;
  });
  let total = `total ${usd(u.total_cost_usd)}`;
  if (u.unpriced?.length) total += " (excluding unpriced models; add them to ~/.deepagents/prices.json)";
  return [...lines, total].join("\n");
}
