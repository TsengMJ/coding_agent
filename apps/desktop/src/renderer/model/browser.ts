import type { PanelState } from "../desktop";

export const hasPage = (s: PanelState): boolean => s.url !== "" && s.url !== "about:blank";

export const displayURL = (url: string): string => (url === "about:blank" ? "" : url);

// normalizeTypedURL turns "localhost:3000" into a URL; the main process
// still refuses anything that is not a local dev server.
export function normalizeTypedURL(text: string): string {
  const t = text.trim();
  if (!t) return "";
  return /^[a-z]+:\/\//i.test(t) ? t : "http://" + t;
}
