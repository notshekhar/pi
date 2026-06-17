import { describe, expect, test } from "bun:test";
import { findDeniedCommand, formatDenyRefusal } from "../src/tools/utils/command-deny";

const deny = ["rm", "git commit", "sudo"];

describe("findDeniedCommand", () => {
    test("matches a bare command", () => {
        expect(findDeniedCommand("rm file.txt", deny)).toBe("rm");
    });

    test("matches command + subcommand prefix", () => {
        expect(findDeniedCommand('git commit -m "x"', deny)).toBe("git commit");
    });

    test("does not match a sibling subcommand", () => {
        expect(findDeniedCommand("git status", deny)).toBeNull();
    });

    test("allows a command not on the list", () => {
        expect(findDeniedCommand("ls -la", deny)).toBeNull();
    });

    test("resolves a full path to its basename", () => {
        expect(findDeniedCommand("/bin/rm -rf build", deny)).toBe("rm");
    });

    test("sees past leading env assignments and wrappers", () => {
        expect(findDeniedCommand("FOO=1 sudo rm -rf /tmp/x", deny)).toBe("rm");
    });

    test("catches a denied command later in a pipeline", () => {
        expect(findDeniedCommand("cat list.txt && rm list.txt", deny)).toBe("rm");
    });

    test("catches a denied command inside command substitution", () => {
        expect(findDeniedCommand("echo $(git commit -m hi)", deny)).toBe("git commit");
    });

    test("sees through the rtk token-proxy wrapper", () => {
        expect(findDeniedCommand("rtk git commit -m x", deny)).toBe("git commit");
    });

    test("sees through rtk proxy passthrough", () => {
        expect(findDeniedCommand("rtk proxy git commit", deny)).toBe("git commit");
    });

    test("catches a denied command inside sh -c", () => {
        expect(findDeniedCommand("bash -c 'git commit -m hi'", deny)).toBe("git commit");
        expect(findDeniedCommand('sh -c "rm -rf build"', deny)).toBe("rm");
    });

    test("peels interleaved env-assignments and wrappers", () => {
        expect(findDeniedCommand("env FOO=1 sudo rtk rm x", deny)).toBe("rm");
    });

    test("an empty denylist allows everything", () => {
        expect(findDeniedCommand("rm -rf /", [])).toBeNull();
    });

    test("tolerates legacy {pattern,reason} object entries from older settings", () => {
        // Cast: persisted JSON may still hold the old object form.
        const legacy = [{ pattern: "rm" }] as unknown as string[];
        expect(findDeniedCommand("rm x", legacy)).toBe("rm");
    });
});

describe("formatDenyRefusal", () => {
    test("names the command, frames it as intentional, forbids workarounds", () => {
        const text = formatDenyRefusal("git commit");
        expect(text).toContain("`git commit` is blocked");
        expect(text).toContain("intentional");
        expect(text).toContain("equivalent");
    });

    test("is short (2-3 lines)", () => {
        expect(formatDenyRefusal("git commit").split("\n").length).toBeLessThanOrEqual(3);
    });

    test("offers asking the user to run it or remove it from the denylist", () => {
        const text = formatDenyRefusal("git commit");
        expect(text).toContain("remove it from their denylist");
    });
});
