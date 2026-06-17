import { describe, expect, test } from "bun:test";
import { findDeniedCommand, formatDenyRefusal } from "../src/tools/utils/command-deny";

const deny = ["rm", "git commit", "sudo"];

describe("findDeniedCommand", () => {
    test("matches a bare command", () => {
        expect(findDeniedCommand("rm file.txt", deny)?.pattern).toBe("rm");
    });

    test("matches command + subcommand prefix", () => {
        expect(findDeniedCommand('git commit -m "x"', deny)?.pattern).toBe("git commit");
    });

    test("does not match a sibling subcommand", () => {
        expect(findDeniedCommand("git status", deny)).toBeNull();
    });

    test("allows a command not on the list", () => {
        expect(findDeniedCommand("ls -la", deny)).toBeNull();
    });

    test("resolves a full path to its basename", () => {
        expect(findDeniedCommand("/bin/rm -rf build", deny)?.pattern).toBe("rm");
    });

    test("sees past leading env assignments and wrappers", () => {
        expect(findDeniedCommand("FOO=1 sudo rm -rf /tmp/x", deny)?.pattern).toBe("rm");
    });

    test("catches a denied command later in a pipeline", () => {
        expect(findDeniedCommand("cat list.txt && rm list.txt", deny)?.pattern).toBe("rm");
    });

    test("catches a denied command inside command substitution", () => {
        expect(findDeniedCommand("echo $(git commit -m hi)", deny)?.pattern).toBe("git commit");
    });

    test("sees through the rtk token-proxy wrapper", () => {
        expect(findDeniedCommand("rtk git commit -m x", deny)?.pattern).toBe("git commit");
    });

    test("sees through rtk proxy passthrough", () => {
        expect(findDeniedCommand("rtk proxy git commit", deny)?.pattern).toBe("git commit");
    });

    test("catches a denied command inside sh -c", () => {
        expect(findDeniedCommand("bash -c 'git commit -m hi'", deny)?.pattern).toBe("git commit");
        expect(findDeniedCommand('sh -c "rm -rf build"', deny)?.pattern).toBe("rm");
    });

    test("peels interleaved env-assignments and wrappers", () => {
        expect(findDeniedCommand("env FOO=1 sudo rtk rm x", deny)?.pattern).toBe("rm");
    });

    test("an empty denylist allows everything", () => {
        expect(findDeniedCommand("rm -rf /", [])).toBeNull();
    });

    test("carries the entry's custom reason", () => {
        const match = findDeniedCommand("curl example.com", [{ pattern: "curl", reason: "use the fetch tool" }]);
        expect(match?.reason).toBe("use the fetch tool");
    });
});

describe("formatDenyRefusal", () => {
    test("names the command and forbids workarounds", () => {
        const text = formatDenyRefusal({ pattern: "git commit" });
        expect(text).toContain("`git commit` is blocked");
        expect(text).toContain("intentional restriction");
        expect(text).toContain("equivalent");
    });

    test("appends the reason when present", () => {
        const text = formatDenyRefusal({ pattern: "curl", reason: "use the fetch tool" });
        expect(text).toContain("Reason: use the fetch tool");
    });
});
