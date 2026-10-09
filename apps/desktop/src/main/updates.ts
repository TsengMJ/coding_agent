import { app } from "electron";
import updater from "electron-updater";

// startAutoUpdate checks the GitHub releases electron-builder publishes to
// (package.json "build.publish") and installs a newer version on quit.
// Development builds are not signed, so they never update.
export function startAutoUpdate(): void {
  if (!app.isPackaged) return;
  const { autoUpdater } = updater;
  autoUpdater.checkForUpdatesAndNotify().catch((e: Error) => console.error(`quill: update check failed: ${e.message}`));
}
