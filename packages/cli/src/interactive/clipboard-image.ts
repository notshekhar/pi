/**
 * Read an image from the system clipboard on macOS via osascript.
 * Returns the saved temp-file path, or null if no image in clipboard / not on macOS.
 */
import { spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { existsSync, statSync, unlinkSync } from "node:fs";
import { randomUUID } from "node:crypto";

export function readClipboardImageToFile(): string | null {
  if (process.platform !== "darwin") return null;

  const out = join(tmpdir(), `pi-clipboard-${randomUUID()}.png`);
  // AppleScript: try to coerce clipboard to PNG, write to tmp file.
  const script = `try
  set thePNG to the clipboard as «class PNGf»
  set theFile to open for access POSIX file "${out}" with write permission
  set eof of theFile to 0
  write thePNG to theFile
  close access theFile
  return "${out}"
on error errMsg
  try
    close access POSIX file "${out}"
  end try
  return ""
end try`;

  const result = spawnSync("osascript", ["-e", script], { encoding: "utf8" });
  const path = result.stdout?.trim();
  if (!path || !existsSync(path)) {
    if (existsSync(out)) {
      try {
        unlinkSync(out);
      } catch {}
    }
    return null;
  }
  try {
    if (statSync(path).size === 0) {
      unlinkSync(path);
      return null;
    }
  } catch {
    return null;
  }
  return path;
}
