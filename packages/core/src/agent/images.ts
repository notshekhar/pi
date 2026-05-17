/**
 * Image attachment detection in user input.
 * Looks for paths to common image files in the message text, reads them,
 * and returns ai-sdk image content blocks plus the cleaned-up text.
 */
import { existsSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp)\b/i;
// matches absolute / relative / ~/ paths ending in an image ext
const PATH_RE = /(?:^|[\s,()'"]|@)((?:~|\.{0,2}\/[^\s,()'"]+|\/[^\s,()'"]+)\.(?:png|jpe?g|gif|webp|bmp))/gi;

export interface ExtractedImages {
  textWithoutPaths: string;
  images: Array<{ data: Buffer; mediaType: string; path: string }>;
}

function mediaTypeFromPath(p: string): string {
  const ext = p.toLowerCase().match(/\.([a-z]+)$/)?.[1];
  switch (ext) {
    case "jpg":
    case "jpeg":
      return "image/jpeg";
    case "gif":
      return "image/gif";
    case "webp":
      return "image/webp";
    case "bmp":
      return "image/bmp";
    case "png":
    default:
      return "image/png";
  }
}

function expandHome(p: string): string {
  if (p.startsWith("~")) return (process.env.HOME ?? "") + p.slice(1);
  return p;
}

export function extractImagesFromInput(input: string, cwd: string): ExtractedImages {
  const images: ExtractedImages["images"] = [];
  const seen = new Set<string>();
  let cleaned = input;

  // Quick reject if no image extension present
  if (!IMAGE_EXT.test(input)) {
    return { textWithoutPaths: input, images: [] };
  }

  for (const m of input.matchAll(PATH_RE)) {
    const raw = m[1];
    const abs = resolve(cwd, expandHome(raw));
    if (seen.has(abs)) continue;
    seen.add(abs);
    if (!existsSync(abs)) continue;
    try {
      if (!statSync(abs).isFile()) continue;
      const data = readFileSync(abs);
      images.push({ data, mediaType: mediaTypeFromPath(abs), path: abs });
      cleaned = cleaned.split(raw).join(""); // strip the path from text
    } catch {}
  }

  cleaned = cleaned.replace(/[ \t]{2,}/g, " ").trim();
  return { textWithoutPaths: cleaned, images };
}
