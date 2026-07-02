import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach } from "bun:test";
import { setDbPathForTests } from "../../src/sessions";

/**
 * Give every test in the file its own session DB in a temp dir — the real
 * ~/.loop/loop.db is never touched, and pub-id reuse across tests (fixtures
 * like id "s1") can't collide. bun test shares module state across files, so
 * teardown must restore the default path rather than leave a dead override
 * behind for the next file.
 */
export function useTempSessionDb(): void {
    let dir: string | null = null;
    beforeEach(() => {
        dir = mkdtempSync(join(tmpdir(), "loop-db-"));
        setDbPathForTests(join(dir, "loop.db"));
    });
    afterEach(() => {
        if (!dir) return;
        setDbPathForTests(null);
        rmSync(dir, { recursive: true, force: true });
        dir = null;
    });
}
