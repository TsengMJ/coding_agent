import fs from "node:fs";
import os from "node:os";
import path from "node:path";

export type AgentConfig = { url: string; token: string } | { error: string };

type Env = Record<string, string | undefined>;

export function agentFilePath(home = os.homedir()): string {
  return path.join(home, ".quill", "agent.json");
}

const trimSlash = (u: string) => u.replace(/\/+$/, "");

// readAgentConfig finds the running `quill serve`: $QUILL_SERVER and
// $QUILL_AGENT_TOKEN when set, else ~/.quill/agent.json. It mirrors
// server.ClientToken in the agent: the file token is only sent to the server
// that wrote it.
export function readAgentConfig(env: Env = process.env, file = agentFilePath()): AgentConfig {
  let published: { url: string; token: string } | undefined;
  try {
    const raw = JSON.parse(fs.readFileSync(file, "utf8")) as { url?: unknown; token?: unknown };
    if (typeof raw.url === "string" && typeof raw.token === "string") {
      published = { url: trimSlash(raw.url), token: raw.token };
    } else {
      return { error: `${file} has no url and token; restart \`quill serve\`.` };
    }
  } catch (e) {
    if ((e as NodeJS.ErrnoException).code !== "ENOENT") {
      return { error: `Cannot read ${file}: ${(e as Error).message}` };
    }
  }
  const envToken = env.QUILL_AGENT_TOKEN || undefined;
  if (env.QUILL_SERVER) {
    const url = trimSlash(env.QUILL_SERVER);
    const token = envToken ?? (published?.url === url ? published.token : undefined);
    if (!token) return { error: `QUILL_SERVER is ${url} but there is no token for it; set QUILL_AGENT_TOKEN.` };
    return { url, token };
  }
  if (!published) {
    return { error: "The agent service is not running. Start it with `quill serve` (or `make serve`) in your project directory." };
  }
  return { url: published.url, token: envToken ?? published.token };
}
