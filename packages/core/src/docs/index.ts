/**
 * Internal docs surfaced through the read tool's `loop://docs/...` scheme.
 *
 * Doc bodies live in `.md` files next to this one and are inlined into the
 * bundle via the generated `generated.ts` string map (run `bun run gen:docs`
 * after editing any .md). Keeping the bodies in generated.ts — rather than an
 * `import ... with { type: "text" }` — avoids import attributes that some TS
 * language servers reject. To add a doc: drop a `.md` file here, re-run
 * gen:docs, and add a SUMMARIES entry. `loop://docs` lists them.
 */
import { DOCS_CONTENT } from "./generated";

export interface DocEntry {
    /** Filename used in the URI, e.g. "config.md". */
    name: string;
    /** One-line description shown in the `loop://docs` index. */
    summary: string;
    /** Full markdown body. */
    content: string;
}

/** One-line descriptions for the `loop://docs` index, keyed by filename. */
const SUMMARIES: Record<string, string> = {
    "config.md": "Configure loop: add models, custom providers, hooks, MCP servers, and custom agents.",
};

export const DOCS: Record<string, DocEntry> = Object.fromEntries(
    Object.entries(DOCS_CONTENT).map(([name, content]) => [name, { name, summary: SUMMARIES[name] ?? name, content }]),
);

export function listDocs(): DocEntry[] {
    return Object.values(DOCS);
}

/** Look up a doc by name; `.md` suffix is optional in the lookup. */
export function getDoc(name: string): DocEntry | undefined {
    const key = name.endsWith(".md") ? name : `${name}.md`;
    return DOCS[key];
}

/** Rendered index for `loop://docs` — the discovery entry point for agents. */
export function renderDocsIndex(): string {
    const lines = listDocs().map((d) => `- loop://docs/${d.name} — ${d.summary}`);
    return ["Internal loop docs. Read one with the read tool, e.g. read loop://docs/config.md", "", ...lines].join(
        "\n",
    );
}
