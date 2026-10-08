import { describe, expect, it } from "vitest";
import {
  base64FromDataURL,
  imageError,
  imageSrc,
  imagesOf,
  MAX_IMAGE_BYTES,
  pendingText,
  userContent,
  type ImageAttachment,
} from "./images";
import type { Message } from "../types";

const img = (size: number): ImageAttachment => ({ name: "a.png", mimeType: "image/png", base64: "AAAA", size });

describe("imageError", () => {
  it("accepts a small PNG", () => {
    expect(imageError({ name: "a.png", type: "image/png", size: 1000 }, [])).toBe("");
  });

  it("rejects other file types", () => {
    expect(imageError({ name: "a.pdf", type: "application/pdf", size: 10 }, [])).toContain("only PNG");
  });

  it("rejects an image over the per-image limit", () => {
    expect(imageError({ name: "big.jpg", type: "image/jpeg", size: MAX_IMAGE_BYTES + 1 }, [])).toContain("limited to 5 MB");
  });

  it("rejects an image that pushes the total over the limit", () => {
    expect(imageError({ name: "c.png", type: "image/png", size: 4 << 20 }, [img(4 << 20), img(4 << 20)])).toContain("the limit is 10 MB");
  });
});

describe("userContent", () => {
  it("puts the text before the images", () => {
    expect(userContent("look", [img(1)])).toEqual([
      { type: "text", text: "look" },
      { type: "image", mime_type: "image/png", base64: "AAAA" },
    ]);
  });

  it("omits an empty text block", () => {
    expect(userContent("", [img(1)])).toEqual([{ type: "image", mime_type: "image/png", base64: "AAAA" }]);
  });
});

describe("image helpers", () => {
  it("strips the data URL prefix", () => {
    expect(base64FromDataURL("data:image/png;base64,QUJD")).toBe("QUJD");
  });

  it("finds image blocks and renders their source", () => {
    const blocks = imagesOf({ role: "user", content: userContent("x", [img(1)]) });
    expect(blocks.map(imageSrc)).toEqual(["data:image/png;base64,AAAA"]);
  });

  it("finds screenshots in tool results", () => {
    const shot: Message = { role: "tool", tool_call_id: "c1", content: [{ type: "text", text: "Took a screenshot" }, { type: "image", mime_type: "image/jpeg", base64: "BBBB" }] };
    expect(imagesOf(shot).map(imageSrc)).toEqual(["data:image/jpeg;base64,BBBB"]);
  });

  it("notes attached images in the pending text", () => {
    expect(pendingText("hi", 0)).toBe("hi");
    expect(pendingText("hi", 2)).toBe("hi\n[2 images]");
    expect(pendingText("", 1)).toBe("[1 image]");
  });
});
