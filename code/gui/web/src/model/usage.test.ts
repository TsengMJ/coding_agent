import { describe, expect, it } from "vitest";
import type { ThreadUsage } from "../types";
import { contextText, costText, humanTokens, tokensText } from "./usage";

const base: ThreadUsage = {
  thread_id: "t",
  model: "m",
  context_tokens: 12000,
  context_limit: 100000,
  context_percent: 12,
  conversation_tokens: 7000,
  fixed_tokens: 5000,
  models: [],
  total_cost_usd: 0,
};

describe("usage formatting", () => {
  it("formats token counts", () => {
    expect([humanTokens(800), humanTokens(12345), humanTokens(128000), humanTokens(1_200_000)]).toEqual(["800", "12.3k", "128k", "1.2M"]);
  });

  it("formats /tokens with and without a known window", () => {
    expect(tokensText(base)).toBe("12k / 100k tokens (12.0%) · m");
    expect(tokensText({ ...base, context_limit: undefined, context_tokens: 800 })).toBe("800 tokens · m (context window unknown)");
  });

  it("formats /context", () => {
    expect(contextText(base).split("\n")).toEqual([
      "context 12k / 100k tokens (12.0%) · m",
      "  system prompt + tools  ~5k",
      "  conversation           ~7k",
      "  free                   88k",
    ]);
  });

  it("formats /cost with unpriced models", () => {
    const u: ThreadUsage = {
      ...base,
      models: [
        { model: "openai:gpt-4o", calls: 3, input_tokens: 20000, cache_read_tokens: 5000, output_tokens: 1500, cost_usd: 0.0625 },
        { model: "x:y", calls: 1, input_tokens: 100, output_tokens: 10 },
      ],
      total_cost_usd: 0.0625,
      unpriced: ["x:y"],
    };
    expect(costText(u).split("\n")).toEqual([
      "openai:gpt-4o  3 calls · in 20k (5k cached) · out 1.5k · $0.0625",
      "x:y  1 call · in 100 · out 10 · unpriced",
      "total $0.0625 (excluding unpriced models; add them to ~/.deepagents/prices.json)",
    ]);
    expect(costText(base)).toBe("no model calls yet");
  });
});
