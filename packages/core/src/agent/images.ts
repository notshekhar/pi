/**
 * Image attachment detection in user input.
 * Looks for paths to common image files in the message text, reads them,
 * and returns ai-sdk image content blocks plus the cleaned-up text.
 */
import { existsSync, readFileSync, statSync } from "node:fs";
import { resolve } from "node:path";

const IMAGE_EXT = /\.(png|jpe?g|gif|webp|bmp)\b/i;
// Sentinel form inserted by paste/Ctrl+I/Ctrl+V/`/attach`. Always preferred
// because a leading "/" path otherwise collides with slash commands.
const BRACKET_RE = /\[image:([^\]]+\.(?:png|jpe?g|gif|webp|bmp))\]/gi;
// Bare-path forms (drag-and-drop on terminals that forward path text):
//   /abs/foo.png   ./rel.png  ../rel.png   ~/foo.png
//   'foo bar.png'  "foo bar.png"
//   foo\ bar.png
const PATH_RE =
    /(?:'([^']+\.(?:png|jpe?g|gif|webp|bmp))'|"([^"]+\.(?:png|jpe?g|gif|webp|bmp))"|((?:~|\.{0,2}\/|\/)(?:\\.|[^\s,()'"])+?\.(?:png|jpe?g|gif|webp|bmp)))/gi;

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

    // [image:/path/to/foo.png] — sentinel form. We keep the bracketed token in
    // the text so the transcript is readable; only read bytes here.
    for (const m of input.matchAll(BRACKET_RE)) {
        const captured = m[1];
        if (!captured) continue;
        const abs = resolve(cwd, expandHome(captured));
        if (seen.has(abs)) continue;
        seen.add(abs);
        if (!existsSync(abs)) continue;
        try {
            if (!statSync(abs).isFile()) continue;
            const data = readFileSync(abs);
            images.push({ data, mediaType: mediaTypeFromPath(abs), path: abs });
        } catch {}
    }

    for (const m of input.matchAll(PATH_RE)) {
        const matchedText = m[0];
        // Capture group 1: single-quoted, 2: double-quoted, 3: bare/escaped
        const captured = m[1] ?? m[2] ?? m[3];
        if (!captured) continue;
        // Unescape backslash-escaped chars (drag-and-drop produces "foo\ bar.png")
        const unescaped = captured.replace(/\\(.)/g, "$1");
        const abs = resolve(cwd, expandHome(unescaped));
        if (seen.has(abs)) continue;
        seen.add(abs);
        if (!existsSync(abs)) continue;
        try {
            if (!statSync(abs).isFile()) continue;
            const data = readFileSync(abs);
            images.push({ data, mediaType: mediaTypeFromPath(abs), path: abs });
            cleaned = cleaned.split(matchedText).join("");
        } catch {}
    }

    cleaned = cleaned.replace(/[ \t]{2,}/g, " ").trim();
    return { textWithoutPaths: cleaned, images };
}
