import { describe, expect, it } from "vitest";
import { prTitle, repoName, splitPatch, statusLabel, toPageCoords, totals } from "./remote";

const patch = `diff --git a/README.md b/README.md
index 1..2 100644
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-hello
+hello world
diff --git a/new.txt b/new.txt
new file mode 100644
--- /dev/null
+++ b/new.txt
@@ -0,0 +1 @@
+new
diff --git a/old.txt b/old.txt
deleted file mode 100644
--- a/old.txt
+++ /dev/null
@@ -1 +0,0 @@
-old
diff --git a/logo.png b/logo.png
Binary files a/logo.png and b/logo.png differ
`;

describe("splitPatch", () => {
  it("keys each file's diff by its path", () => {
    const files = splitPatch(patch);
    expect([...files.keys()]).toEqual(["README.md", "new.txt", "old.txt", "logo.png"]);
    expect(files.get("README.md")).toContain("+hello world");
    expect(files.get("README.md")).not.toContain("new.txt");
    expect(files.get("old.txt")).toContain("-old");
  });

  it("returns nothing for an empty patch", () => {
    expect(splitPatch("").size).toBe(0);
  });
});

describe("Files changed summary", () => {
  it("adds up the changes and labels the status", () => {
    const files = [
      { path: "a", status: "modified", additions: 3, deletions: 1 },
      { path: "b", status: "added", additions: 2, deletions: 0 },
    ];
    expect(totals(files)).toEqual({ additions: 5, deletions: 1 });
    expect(statusLabel("renamed")).toBe("R");
    expect(statusLabel("weird")).toBe("?");
  });
});

describe("toPageCoords", () => {
  it("scales a click on the shown screenshot to page pixels", () => {
    expect(toPageCoords({ x: 320, y: 90 }, { width: 640, height: 360 }, { width: 1280, height: 720 })).toEqual({ x: 640, y: 180 });
  });

  it("ignores clicks before the image has a size or outside it", () => {
    expect(toPageCoords({ x: 1, y: 1 }, { width: 0, height: 0 }, { width: 1280, height: 720 })).toBeNull();
    expect(toPageCoords({ x: -5, y: 1 }, { width: 640, height: 360 }, { width: 1280, height: 720 })).toBeNull();
  });
});

describe("labels", () => {
  it("names GitHub repositories by owner/repo", () => {
    expect(repoName("https://github.com/acme/app")).toBe("acme/app");
    expect(repoName("https://github.com/acme/app.git")).toBe("acme/app");
    expect(repoName("https://gitlab.com/acme/app")).toBe("https://gitlab.com/acme/app");
  });

  it("suggests a PR title from the first line of the request", () => {
    expect(prTitle("Fix the login bug\nmore detail")).toBe("Fix the login bug");
    expect(prTitle("x".repeat(100))).toHaveLength(72);
  });
});
