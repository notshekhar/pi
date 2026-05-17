import { mkdirSync, readdirSync, readFileSync, statSync, appendFileSync, existsSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { ulid } from "ulid";
import lockfile from "proper-lockfile";
import { getPiDir, settingsStore } from "../auth/storage";
import type { Entry, ProviderId, SessionInfoData } from "../types";
import { adaptPiEntry } from "./pi-adapter";

function slugCwd(cwd: string): string {
  // pi convention: "--Users-notshekhar-Documents-foo--"
  const stripped = cwd.replace(/^\/+|\/+$/g, "").replace(/\//g, "-");
  return `--${stripped}--`;
}

function sessionsDir(): string {
  const dir = join(getPiDir(), "agent", "sessions");
  mkdirSync(dir, { recursive: true });
  return dir;
}

export interface SessionInfo extends SessionInfoData {
  path: string;
  mtime: number;
  firstUserMessage?: string;
  source: "pi-agent" | "pi";
}

export interface NewSessionOptions {
  cwd: string;
  provider: ProviderId;
  model: string;
}

export class Session {
  readonly id: string;
  readonly info: SessionInfoData;
  readonly path: string;
  private buffered: Entry[] = [];

  constructor(info: SessionInfoData, path: string, buffered: Entry[]) {
    this.id = info.id;
    this.info = info;
    this.path = path;
    this.buffered = buffered;
  }

  static load(path: string, info: SessionInfoData): Session {
    const raw = existsSync(path) ? readFileSync(path, "utf8") : "";
    const lines = raw.split("\n").filter(Boolean);
    const entries: Entry[] = [];
    for (const line of lines) {
      try {
        const parsed = JSON.parse(line);
        const adapted = adaptPiEntry(parsed);
        if (adapted) entries.push(adapted);
      } catch {}
    }
    return new Session(info, path, entries);
  }

  entries(): Entry[] {
    return [...this.buffered];
  }

  async append(entry: Entry): Promise<void> {
    this.buffered.push(entry);
    const dir = join(this.path, "..");
    mkdirSync(dir, { recursive: true });
    if (!existsSync(this.path)) writeFileSync(this.path, "");
    const release = await lockfile.lock(this.path, { retries: { retries: 5, minTimeout: 50, maxTimeout: 200 } });
    try {
      appendFileSync(this.path, JSON.stringify(entry) + "\n");
    } finally {
      await release();
    }
  }

  messages(): Array<{ role: "user" | "assistant" | "tool"; content: unknown }> {
    return this.buffered
      .filter((e): e is Extract<Entry, { type: "message" }> => e.type === "message")
      .map((e) => ({ role: e.role, content: e.content }));
  }

  lastCompactCutAt(): number {
    let cut = 0;
    for (const e of this.buffered) {
      if (e.type === "compact") cut = Math.max(cut, e.cutAt);
    }
    return cut;
  }
}

export class SessionManager {
  list(cwd?: string): SessionInfo[] {
    const root = sessionsDir();
    if (!existsSync(root)) return [];
    const out: SessionInfo[] = [];
    const slugs = cwd ? [slugCwd(cwd)] : readdirSync(root);
    for (const slug of slugs) {
      const dir = join(root, slug);
      if (!existsSync(dir)) continue;
      const stat = statSync(dir);
      if (!stat.isDirectory()) continue;
      for (const file of readdirSync(dir)) {
        if (!file.endsWith(".jsonl")) continue;
        const path = join(dir, file);
        const info = this.peek(path, slug);
        if (info) out.push(info);
      }
    }
    return out.sort((a, b) => b.mtime - a.mtime);
  }

  private peek(path: string, slug: string): SessionInfo | null {
    try {
      const raw = readFileSync(path, "utf8");
      const lines = raw.split("\n").filter(Boolean);
      let info: SessionInfoData | null = null;
      let firstUser: string | undefined;
      let source: "pi-agent" | "pi" = "pi";
      for (const line of lines) {
        const parsed = JSON.parse(line) as { type?: string };
        if (parsed.type === "session-info") {
          info = parsed as unknown as SessionInfoData;
          source = "pi-agent";
        }
        if (parsed.type === "message") {
          const m = parsed as { role?: string; content?: unknown };
          if (m.role === "user" && !firstUser) {
            firstUser = typeof m.content === "string" ? m.content : JSON.stringify(m.content).slice(0, 120);
          }
        }
      }
      const stat = statSync(path);
      if (!info) {
        const id = path.split("/").pop()!.replace(".jsonl", "");
        info = { id, createdAt: stat.birthtimeMs, cwd: slug, provider: "xai", model: "" };
      }
      return { ...info, path, mtime: stat.mtimeMs, firstUserMessage: firstUser, source };
    } catch {
      return null;
    }
  }

  async create(opts: NewSessionOptions): Promise<Session> {
    const id = ulid();
    const slug = slugCwd(opts.cwd);
    const dir = join(sessionsDir(), slug);
    mkdirSync(dir, { recursive: true });
    const path = join(dir, `${id}.jsonl`);
    const info: SessionInfoData = {
      id,
      createdAt: Date.now(),
      cwd: opts.cwd,
      provider: opts.provider,
      model: opts.model,
    };
    const session = new Session(info, path, []);
    await session.append({ type: "session-info", ts: Date.now(), ...info });
    return session;
  }

  async open(idOrPath: string): Promise<Session> {
    const path = idOrPath.endsWith(".jsonl") ? idOrPath : this.findById(idOrPath);
    if (!path) throw new Error(`Session not found: ${idOrPath}`);
    const peek = this.peek(path, "");
    if (!peek) throw new Error(`Cannot read session: ${path}`);
    const session = Session.load(path, peek);

    if (peek.source === "pi") {
      const mode = (settingsStore.get("piCompatMode") as string) ?? "fork";
      if (mode === "fork") {
        return this.fork(session);
      }
    }
    return session;
  }

  private findById(id: string): string | null {
    const root = sessionsDir();
    for (const slug of readdirSync(root)) {
      const candidate = join(root, slug, `${id}.jsonl`);
      if (existsSync(candidate)) return candidate;
    }
    return null;
  }

  async fork(source: Session): Promise<Session> {
    const newId = ulid();
    const dir = join(source.path, "..");
    const newPath = join(dir, `${newId}.jsonl`);
    const info: SessionInfoData = { ...source.info, id: newId, createdAt: Date.now() };
    writeFileSync(newPath, "");
    const forked = new Session(info, newPath, []);
    await forked.append({ type: "session-info", ts: Date.now(), ...info });
    for (const entry of source.entries()) {
      if (entry.type === "session-info") continue;
      await forked.append(entry);
    }
    return forked;
  }
}
