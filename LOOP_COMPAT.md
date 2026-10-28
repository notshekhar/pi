# One home with loop

pi runs on loop's own `~/.loop`. Same files, same formats, same database —
a session started in loop resumes in pi and the other way round, and pi
creates nothing of its own. There is no `~/.pi-agent`.

Rules that make sharing safe:

- **Never bump `agent.db`'s `schema_version`.** loop refuses a database stamped
  newer than it knows; pi must open v7 and write v7.
- **Write exactly loop's shapes.** An entry payload pi writes must be one loop
  would have written: same keys, same nesting, `providerOptions` not `options`.
- **Preserve what pi does not understand.** Entries and JSON files carry keys
  pi has no use for (`team`, `timing.tools`, `refs`, `customMeta`, …). They are
  kept verbatim on read and never dropped on write.
- **Develop against a copy.** `CHILD_HOME=<scratch>/loophome` (a consistent
  copy of `~/.loop`, `sqlite3 .backup` for the db) — never the live home.

Status: **[x]** compatible · **[~]** partial · **[ ]** not yet

## Database — `~/.loop/agent.db` (WAL, schema v7)

| table | loop owner | pi |
|---|---|---|
| `sessions` (pub_id, cwd, provider, model, name, parent_pub, archived_at) | sessions/sqlite-store.ts | [ ] |
| `entries` — a TREE (`pub_id`, `parent_pub_id`), payload = full entry JSON, usage columns derived at insert | sessions/session.ts | [ ] |
| `cost_ledger` — one row per billed round-trip, with price snapshot | sessions/cost-ledger.ts | [ ] |
| `projects` — per-dir trust, model, provider_models, bash_allow | sessions/projects.ts | [ ] |
| `reminders` | reminders | [ ] |
| `teams`, `team_members`, `team_messages`, `goals` | teams/, goals/ | [ ] (preserved, unused) |

Entry types: `session-info`, `message`, `subagent`, `model-change`, `compact`,
`branch-summary`, `label`, `session-name`, `custom`.

## Files

| path | what | pi |
|---|---|---|
| `auth.json` | provider keys, OAuth, custom providers | [~] keys read today |
| `settings.json` | loop's settings schema (`defaultModel`, `thinkingLevel`, `pinnedInput`, `hooks`, `mcpServers`, `bashAllow`, …) | [ ] |
| `catalog.json` | model availability + models.dev defs (`availability`, `ts`, `models`, `modelsTs`, `refs`, `customMeta`, …) | [ ] |
| `models.json` | user model overrides | [ ] |
| `agents/` | custom agents | [ ] |
| `agent/skills/`, `<repo>/.loop/skills` | skills | [ ] |
| `agent/prompts/` | prompt commands | [ ] |
| `agent/themes/` | user themes | [ ] |
| `AGENTS.md`, `CLAUDE.md` (global) + repo context files | workspace context | [ ] |
| `trust.json` | trusted projects | [ ] |
| `extensions.json`, `extensions/` | extension enablement | [ ] |
| `recipes/`, `artifacts/`, `plans/` | recipes, agent-written pages, plans | [ ] |
| `native-agents.json`, `native-agent-sessions.json` | native agent probes/links | [ ] |
| `servers/` | LSP downloads | [ ] |
| `shells/`, `bin/` | background shells, shims | [ ] |
| `<repo>/.loop/settings.json`, `<repo>/.loop/mcp.json` | project hooks, permissions, MCP | [ ] |
