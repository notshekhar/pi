/**
 * Skill loading + system-prompt formatting.
 * Reuses pi-mono's `loadSkills` and `formatSkillsForPrompt` so behavior matches pi exactly.
 *
 * Skill discovery locations:
 *   - ~/.pi/agent/skills/        (user-global)
 *   - <cwd>/.pi/skills/          (project-local)
 * Frontmatter spec: name, description, optional disable-model-invocation
 */
import { loadSkills, formatSkillsForPrompt, type Skill } from "@earendil-works/pi-coding-agent";
import { join } from "node:path";
import { getPiDir } from "../auth/storage";

export type { Skill };

export interface LoadedSkills {
  skills: Skill[];
  diagnostics: unknown[];
  promptBlock: string;
}

export function loadProjectSkills(cwd: string): LoadedSkills {
  const agentDir = join(getPiDir(), "agent");
  try {
    const result = loadSkills({
      cwd,
      agentDir,
      skillPaths: [],
      includeDefaults: true,
    });
    return {
      skills: result.skills,
      diagnostics: result.diagnostics,
      promptBlock: formatSkillsForPrompt(result.skills),
    };
  } catch {
    return { skills: [], diagnostics: [], promptBlock: "" };
  }
}
