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

// ConnectionState mirrors src/main/connection.ts.
export type ConnectionState =
  | { mode: "local" }
  | { mode: "remote"; url: string; signedIn: boolean; user?: string };

// DesktopConnection chooses between the local `quill serve` and a team
// server; the main process keeps the session, the page only sees state.
export interface DesktopConnection {
  get(): Promise<ConnectionState>;
  useLocal(): Promise<ConnectionState>;
  // login starts the GitHub device flow and opens the verification page.
  login(url: string): Promise<{ user_code: string; verification_uri: string }>;
  // wait resolves once the user entered the code on GitHub.
  wait(): Promise<ConnectionState>;
  logout(): Promise<ConnectionState>;
}

declare global {
  interface Window {
    quillDesktop?: { browser: DesktopBrowser; connection?: DesktopConnection };
  }
}

export const desktopBrowser = (): DesktopBrowser | undefined => window.quillDesktop?.browser;
export const desktopConnection = (): DesktopConnection | undefined => window.quillDesktop?.connection;
