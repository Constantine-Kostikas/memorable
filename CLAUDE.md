# memorable

Persistent, searchable memory for Claude Code over MCP, written in Go.

- Design spec: `docs/design.md` (binding authority).
- Implementation plan: `docs/superpowers/plans/2026-11-14-memorable-v1.md`.

## Go development

Before any Go coding, review, debugging, troubleshooting, or setup task, load the `samber/cc-skills-golang@golang-how-to` skill first: it routes to whichever other Go skills the task needs.

## Commands

- `make test`: unit tests (no Docker).
- `make test-integration`: Postgres integration tests (Docker required).
- `make test-eval`: search evaluation against a local Ollama.
- `make lint`, `make vuln`, `make sec`: static checks.
