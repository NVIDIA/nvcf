# Agent Memory

`.memory/` contains agent-operating context that should change more often than `AGENTS.md`. Treat these files as active workspace state, not as a replacement for source code, tests, README contracts, or durable docs.

## Startup Checklist

- Read `AGENTS.md` first.
- Read this file before using or editing memory.
- Read `TASK_TEMPLATE.md` before substantial implementation work.
- Read `current.md` for handoff state and in-flight work.
- Read `TODO.md` for active backlog.
- Read `lessons.md` for durable project lessons and local coding conventions.
- Read `tools.md` for reusable local commands and scripts.

## File Roles

- `current.md`: current task state, handoff notes, touched files, verification status, and blockers.
- `TASK_TEMPLATE.md`: planning template for substantial tasks; use it to decide whether enough context exists to proceed.
- `TODO.md`: active product and engineering backlog with acceptance criteria.
- `lessons.md`: concise lessons learned from prior work that should guide future edits.
- `tools.md`: reusable commands, scripts, and workflow notes.
- `scripts/`: small helper scripts created for repeated agent workflows. Prefer the repo-level `scripts/` directory for supported project tooling.

## Update Rules

- Update `current.md` when starting or resuming substantial multi-step work, before handing off, and when blockers or verification status change.
- For substantial implementation, fill the task template mentally or copy the relevant fields into `current.md`. If goal, acceptance criteria, test strategy, or verification commands are unclear after reading local context, ask the user concise clarifying questions before proceeding.
- Move durable backlog into `TODO.md`; do not bury product work only in `current.md`.
- Add to `lessons.md` only when the lesson is non-obvious, likely reusable, and not already captured in `AGENTS.md`.
- Add to `tools.md` when a command sequence or helper becomes reusable. Include prerequisites and expected outputs.
- Keep entries short. Link to code or docs rather than copying large content.
- Do not store secrets, credentials, large logs, generated artifacts, or transient command output.
- After changing `AGENTS.md` or `.memory/`, run `scripts/check_agent_protocol.sh`.
- Periodically prune stale `current.md` notes and completed `TODO.md` entries. Promote stable lessons into durable docs or mechanical checks instead of letting memory grow without bound.
