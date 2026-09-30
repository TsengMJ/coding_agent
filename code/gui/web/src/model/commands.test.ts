import { describe, expect, it } from "vitest";
import type { SlashCommand } from "../types";
import { helpText, matchCommands, resolveCommand } from "./commands";

const cmds: SlashCommand[] = [
  { name: "/compact", aliases: ["/offload"], description: "summarize" },
  { name: "/new", aliases: ["/clear"], description: "new thread" },
  { name: "/resume", args: "<id>", description: "switch thread" },
  { name: "/threads", description: "list threads" },
];

describe("matchCommands", () => {
  it("matches names and aliases by prefix while typing the command word", () => {
    expect(matchCommands("/", cmds)).toHaveLength(4);
    expect(matchCommands("/c", cmds).map((c) => c.name)).toEqual(["/compact", "/new"]);
    expect(matchCommands("/off", cmds).map((c) => c.name)).toEqual(["/compact"]);
  });

  it("closes once arguments start or for plain text", () => {
    expect(matchCommands("/resume ", cmds)).toEqual([]);
    expect(matchCommands("hello", cmds)).toEqual([]);
  });
});

describe("resolveCommand", () => {
  it("follows aliases and splits arguments", () => {
    expect(resolveCommand("/offload", cmds)).toEqual({ kind: "command", name: "/compact", args: "" });
    expect(resolveCommand(" /resume  abc ", cmds)).toEqual({ kind: "command", name: "/resume", args: "abc" });
  });

  it("treats plain text as a message", () => {
    expect(resolveCommand("fix the bug", cmds)).toEqual({ kind: "text" });
  });

  it("suggests the closest command for typos", () => {
    expect(resolveCommand("/thraeds", cmds)).toEqual({ kind: "unknown", name: "/thraeds", suggestion: "/threads" });
    expect(resolveCommand("/zzzzzz", cmds)).toEqual({ kind: "unknown", name: "/zzzzzz", suggestion: undefined });
  });
});

describe("helpText", () => {
  it("shows usage, description and aliases", () => {
    expect(helpText(cmds)).toContain("/resume <id> — switch thread");
    expect(helpText(cmds)).toContain("/compact — summarize (/offload)");
  });
});
