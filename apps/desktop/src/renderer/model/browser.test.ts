import { describe, expect, it } from "vitest";
import { displayURL, hasPage, normalizeTypedURL } from "./browser";

describe("browser pane helpers", () => {
  it("treats about:blank as no page", () => {
    expect(hasPage({ url: "about:blank", title: "", loading: false, canGoBack: false })).toBe(false);
    expect(hasPage({ url: "http://localhost:3000/", title: "", loading: false, canGoBack: false })).toBe(true);
  });

  it("shows an empty address for about:blank", () => {
    expect(displayURL("about:blank")).toBe("");
    expect(displayURL("http://localhost:3000/a")).toBe("http://localhost:3000/a");
  });

  it("adds http:// to a typed host and port", () => {
    expect(normalizeTypedURL("localhost:3000")).toBe("http://localhost:3000");
    expect(normalizeTypedURL(" http://127.0.0.1:5173/x ")).toBe("http://127.0.0.1:5173/x");
    expect(normalizeTypedURL("")).toBe("");
  });
});
