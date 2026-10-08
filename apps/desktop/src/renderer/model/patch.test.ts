import { describe, expect, it } from "vitest";
import { parsePatch, patchPaths } from "./patch";
import { summarize } from "./tools";

const patch = `*** Begin Patch
*** Update File: /src/app.go
*** Move to: /src/main.go
@@ func main() {
 	cfg := load()
-	run(cfg)
+	run2(cfg)
*** Add File: /src/new.go
+package src
*** Delete File: /src/old.go
*** End Patch`;

describe("apply_patch parsing", () => {
  it("splits files and hunks", () => {
    const files = parsePatch(patch)!;
    expect(files.map((f) => [f.op, f.path, f.moveTo])).toEqual([
      ["update", "/src/app.go", "/src/main.go"],
      ["add", "/src/new.go", undefined],
      ["delete", "/src/old.go", undefined],
    ]);
    const hunk = files[0].hunks[0];
    expect(hunk.header).toBe("@@ func main() { @@");
    expect(hunk.lines.map((l) => l.kind)).toEqual(["ctx", "del", "add"]);
    expect(files[1].hunks[0].lines).toEqual([{ kind: "add", text: "package src", newNo: 1 }]);
    expect(files[2].hunks).toEqual([]);
  });

  it("returns null for non-patch text", () => {
    expect(parsePatch("hello")).toBeNull();
  });

  it("summarizes the touched paths", () => {
    expect(patchPaths(patch)).toEqual(["/src/app.go → /src/main.go", "/src/new.go", "/src/old.go"]);
    expect(summarize({ id: "p", name: "apply_patch", args: { patch } })).toBe("/src/app.go → /src/main.go, /src/new.go, /src/old.go");
  });
});
