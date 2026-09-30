import { createContext, useContext } from "react";
import type { Api } from "./api";

// UI is shared by the timeline rows: which rows are expanded, which is
// selected for keyboard navigation, and how to open the LLM inspector.
export interface UI {
  api: Api;
  threadId: string;
  expanded: Set<string>;
  toggle: (key: string) => void;
  selected: string;
  select: (key: string) => void;
  inspect: (seq: number, label: string) => void;
  inspecting?: number;
  query: string;
}

export const UIContext = createContext<UI | null>(null);

export function useUI(): UI {
  const ui = useContext(UIContext);
  if (!ui) throw new Error("useUI outside UIContext");
  return ui;
}
