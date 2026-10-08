// Sandboxed preloads must be CommonJS, hence .cts.
import { contextBridge, ipcRenderer, type IpcRendererEvent } from "electron";

interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

contextBridge.exposeInMainWorld("quillDesktop", {
  browser: {
    setBounds: (rect: Rect | null) => ipcRenderer.send("browser:bounds", rect),
    navigate: (url: string) => ipcRenderer.send("browser:navigate", url),
    back: () => ipcRenderer.send("browser:back"),
    reload: () => ipcRenderer.send("browser:reload"),
    onState: (cb: (state: unknown) => void) => {
      const listener = (_e: IpcRendererEvent, state: unknown) => cb(state);
      ipcRenderer.on("browser:state", listener);
      ipcRenderer.send("browser:hello");
      return () => ipcRenderer.removeListener("browser:state", listener);
    },
  },
});
