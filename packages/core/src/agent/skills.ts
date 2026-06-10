/**
 * Skill loading + system-prompt formatting — own implementation, mirrors
 * pi-mono's core/skills.ts behavior (same discovery rules, same XML prompt
 * format per the Agent Skills standard).
 *
 * Discovery locations:
 *   - ~/.pi/agent/skills/        (user-global)
 *   - <cwd>/.pi/skills/          (project-local)
 * Discovery rules per directory:
 *   - a directory containing SKILL.md is a skill root; don't recurse further
 *   - otherwise load direct *.md children and recurse into subdirectories
 *
 * Divergences from pi-mono (deliberate, to avoid the yaml + ignore deps):
 *   - frontmatter is parsed with a minimal YAML subset (key: value lines,
 *     quoted strings, booleans) — multiline/folded values are not supported
 *   - .gitignore/.ignore files inside skill directories are not honored
 */
import { existsSync, readdirSync, readFileSync, statSync, type Dirent } from "node:fs";
import { basename, dirname, join, resolve } from "node:path";
import { getPiDir } from "../auth/storage";

const MAX_NAME_LENGTH = 64;
const MAX_DESCRIPTION_LENGTH = 1024;

export interface Skill {
  name: string;
  description: string;
  filePath: string;
  baseDir: string;
  disableModelInvocation: boolean;
}

export interface SkillDiagnostic {
  type: "warning" | "collision";
  message: string;
  path: string;
}

export interface LoadedSkills {
  skills: Skill[];
  diagnostics: SkillDiagnostic[];
}

function parseFrontmatter(content: string): Record<string, string | boolean> {
  const normalized = content.replace(/\r\n/g, "\n");
  if (!normalized.startsWith("---")) return {};
  const end = normalized.indexOf("\n---", 3);
  if (end === -1) return {};
  const out: Record<string, string | boolean> = {};
  for (const line of normalized.slice(4, end).split("\n")) {
    const idx = line.indexOf(":");
    if (idx <= 0) continue;
    const key = line.slice(0, idx).trim();
    let value = line.slice(idx + 1).trim();
    if (!key) continue;
    if (value === "true") {
      out[key] = true;
      continue;
    }
    if (value === "false") {
      out[key] = false;
      continue;
    }
    if (
      (value.startsWith('"') && value.endsWith('"') && value.length >= 2) ||
      (value.startsWith("'") && value.endsWith("'") && value.length >= 2)
    ) {
      value = value.slice(1, -1);
    }
    out[key] = value;
  }
  return out;
}

function validateName(name: string): string[] {
  const errors: string[] = [];
  if (name.length > MAX_NAME_LENGTH) errors.push(`name exceeds ${MAX_NAME_LENGTH} characters (${name.length})`);
  if (!/^[a-z0-9-]+$/.test(name)) errors.push("name contains invalid characters (must be lowercase a-z, 0-9, hyphens only)");
  if (name.startsWith("-") || name.endsWith("-")) errors.push("name must not start or end with a hyphen");
  if (name.includes("--")) errors.push("name must not contain consecutive hyphens");
  return errors;
}

function loadSkillFromFile(filePath: string): { skill: Skill | null; diagnostics: SkillDiagnostic[] } {
  const diagnostics: SkillDiagnostic[] = [];
  try {
    const fm = parseFrontmatter(readFileSync(filePath, "utf8"));
    const skillDir = dirname(filePath);
    const description = typeof fm.description === "string" ? fm.description : "";

    if (!description.trim()) {
      diagnostics.push({ type: "warning", message: "description is required", path: filePath });
      return { skill: null, diagnostics };
    }
    if (description.length > MAX_DESCRIPTION_LENGTH) {
      diagnostics.push({
        type: "warning",
        message: `description exceeds ${MAX_DESCRIPTION_LENGTH} characters (${description.length})`,
        path: filePath,
      });
    }

    const name = (typeof fm.name === "string" && fm.name) || basename(skillDir);
    for (const error of validateName(name)) {
      diagnostics.push({ type: "warning", message: error, path: filePath });
    }

    return {
      skill: {
        name,
        description,
        filePath,
        baseDir: skillDir,
        disableModelInvocation: fm["disable-model-invocation"] === true,
      },
      diagnostics,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : "failed to parse skill file";
    diagnostics.push({ type: "warning", message, path: filePath });
    return { skill: null, diagnostics };
  }
}

function loadSkillsFromDir(dir: string, includeRootFiles: boolean): LoadedSkills {
  const skills: Skill[] = [];
  const diagnostics: SkillDiagnostic[] = [];
  if (!existsSync(dir)) return { skills, diagnostics };

  let entries: Dirent[];
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return { skills, diagnostics };
  }

  // SKILL.md marks a skill root — load it and stop recursing
  for (const entry of entries) {
    if (entry.name !== "SKILL.md") continue;
    const fullPath = join(dir, entry.name);
    let isFile = entry.isFile();
    if (entry.isSymbolicLink()) {
      try {
        isFile = statSync(fullPath).isFile();
      } catch {
        continue;
      }
    }
    if (!isFile) continue;
    const result = loadSkillFromFile(fullPath);
    if (result.skill) skills.push(result.skill);
    diagnostics.push(...result.diagnostics);
    return { skills, diagnostics };
  }

  for (const entry of entries) {
    if (entry.name.startsWith(".") || entry.name === "node_modules") continue;
    const fullPath = join(dir, entry.name);

    let isDirectory = entry.isDirectory();
    let isFile = entry.isFile();
    if (entry.isSymbolicLink()) {
      try {
        const stats = statSync(fullPath);
        isDirectory = stats.isDirectory();
        isFile = stats.isFile();
      } catch {
        continue;
      }
    }

    if (isDirectory) {
      const sub = loadSkillsFromDir(fullPath, false);
      skills.push(...sub.skills);
      diagnostics.push(...sub.diagnostics);
      continue;
    }
    if (!isFile || !includeRootFiles || !entry.name.endsWith(".md")) continue;
    const result = loadSkillFromFile(fullPath);
    if (result.skill) skills.push(result.skill);
    diagnostics.push(...result.diagnostics);
  }

  return { skills, diagnostics };
}

export async function loadProjectSkills(cwd: string): Promise<LoadedSkills & { promptBlock: string }> {
  const skillMap = new Map<string, Skill>();
  const diagnostics: SkillDiagnostic[] = [];

  const addSkills = (result: LoadedSkills) => {
    diagnostics.push(...result.diagnostics);
    for (const skill of result.skills) {
      const existing = skillMap.get(skill.name);
      if (existing) {
        if (existing.filePath !== skill.filePath) {
          diagnostics.push({
            type: "collision",
            message: `name "${skill.name}" collision (winner: ${existing.filePath})`,
            path: skill.filePath,
          });
        }
      } else {
        skillMap.set(skill.name, skill);
      }
    }
  };

  addSkills(loadSkillsFromDir(join(getPiDir(), "agent", "skills"), true));
  addSkills(loadSkillsFromDir(resolve(cwd, ".pi", "skills"), true));

  const skills = Array.from(skillMap.values());
  return { skills, diagnostics, promptBlock: formatSkillsForPrompt(skills) };
}

function escapeXml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&apos;");
}

/**
 * XML prompt block per the Agent Skills standard (agentskills.io).
 * Skills with disableModelInvocation are /skill:name-only and excluded.
 */
export function formatSkillsForPrompt(skills: Skill[]): string {
  const visible = skills.filter((s) => !s.disableModelInvocation);
  if (visible.length === 0) return "";

  const lines = [
    "\n\nThe following skills provide specialized instructions for specific tasks.",
    "Use the read tool to load a skill's file when the task matches its description.",
    "When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
    "",
    "<available_skills>",
  ];
  for (const skill of visible) {
    lines.push("  <skill>");
    lines.push(`    <name>${escapeXml(skill.name)}</name>`);
    lines.push(`    <description>${escapeXml(skill.description)}</description>`);
    lines.push(`    <location>${escapeXml(skill.filePath)}</location>`);
    lines.push("  </skill>");
  }
  lines.push("</available_skills>");
  return lines.join("\n");
}
