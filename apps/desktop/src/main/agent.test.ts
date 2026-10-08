import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { readAgentConfig } from "./agent";

function tempFile(content?: object): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "quill-agent-"));
  const file = path.join(dir, "agent.json");
  if (content) fs.writeFileSync(file, JSON.stringify(content));
  return file;
}

describe("readAgentConfig", () => {
  it("reads the file quill serve wrote", () => {
    const file = tempFile({ url: "http://127.0.0.1:8123", token: "t1", pid: 1 });
    expect(readAgentConfig({}, file)).toEqual({ url: "http://127.0.0.1:8123", token: "t1" });
  });

  it("explains how to start the agent when the file is missing", () => {
    const got = readAgentConfig({}, tempFile());
    expect("error" in got && got.error).toContain("quill serve");
  });

  it("lets QUILL_AGENT_TOKEN replace the file token", () => {
    const file = tempFile({ url: "http://127.0.0.1:8123", token: "t1", pid: 1 });
    expect(readAgentConfig({ QUILL_AGENT_TOKEN: "env" }, file)).toEqual({ url: "http://127.0.0.1:8123", token: "env" });
  });

  it("uses QUILL_SERVER with the file token only when the file names that server", () => {
    const file = tempFile({ url: "http://127.0.0.1:8123", token: "t1", pid: 1 });
    expect(readAgentConfig({ QUILL_SERVER: "http://127.0.0.1:8123/" }, file)).toEqual({ url: "http://127.0.0.1:8123", token: "t1" });
    const other = readAgentConfig({ QUILL_SERVER: "http://127.0.0.1:9000" }, file);
    expect("error" in other && other.error).toContain("QUILL_AGENT_TOKEN");
    expect(readAgentConfig({ QUILL_SERVER: "http://127.0.0.1:9000", QUILL_AGENT_TOKEN: "x" }, file)).toEqual({
      url: "http://127.0.0.1:9000",
      token: "x",
    });
  });

  it("reports a corrupt file", () => {
    const file = tempFile();
    fs.writeFileSync(file, "{nope");
    const got = readAgentConfig({}, file);
    expect("error" in got && got.error).toContain(file);
  });
});
