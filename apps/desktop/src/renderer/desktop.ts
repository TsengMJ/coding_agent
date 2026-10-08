// The Electron preload (src/main/preload.cts) exposes window.quillDesktop;
// it is missing when the renderer runs in a plain browser.

export interface PanelState {
  url: string;
  title: string;
  loading: boolean;
  canGoBack: boolean;
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface DesktopBrowser {
  setBounds(rect: Rect | null): void;
  navigate(url: string): void;
  back(): void;
  reload(): void;
  onState(cb: (state: PanelState) => void): () => void;
}

declare global {
  interface Window {
    quillDesktop?: { browser: DesktopBrowser };
  }
}

export const desktopBrowser = (): DesktopBrowser | undefined => window.quillDesktop?.browser;
