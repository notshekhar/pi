export interface Args {
    cmd?: string;
    positional: string[];
    flags: Record<string, string | boolean>;
}

export function parseArgs(argv: string[]): Args {
    const out: Args = { positional: [], flags: {} };
    let i = 0;
    if (argv[0] && !argv[0].startsWith("-")) {
        out.cmd = argv[0];
        i = 1;
    }
    for (; i < argv.length; i++) {
        const a = argv[i];
        if (a === "-v") {
            out.flags.v = true;
            continue;
        }
        if (a === "-h") {
            out.flags.h = true;
            continue;
        }
        if (a.startsWith("--")) {
            const eq = a.indexOf("=");
            if (eq > 0) {
                out.flags[a.slice(2, eq)] = a.slice(eq + 1);
            } else {
                const next = argv[i + 1];
                if (next && !next.startsWith("--")) {
                    out.flags[a.slice(2)] = next;
                    i++;
                } else {
                    out.flags[a.slice(2)] = true;
                }
            }
        } else {
            out.positional.push(a);
        }
    }
    return out;
}

export async function readStdinLine(prompt: string): Promise<string> {
    process.stdout.write(prompt);
    return new Promise((resolve) => {
        let data = "";
        process.stdin.setEncoding("utf8");
        const onData = (chunk: string) => {
            data += chunk;
            const nl = data.indexOf("\n");
            if (nl >= 0) {
                process.stdin.off("data", onData);
                process.stdin.pause();
                resolve(data.slice(0, nl).trim());
            }
        };
        process.stdin.resume();
        process.stdin.on("data", onData);
    });
}
