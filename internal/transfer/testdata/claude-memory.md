---
name: v2-multi-user-scope
description: Multi-user/shared-server support for memorable is deferred to v2; v1 is single-user local only
metadata:
  node_type: memory
  type: project
  originSessionId: 00000000-0000-0000-0000-000000000000
  modified: 2026-11-14T16:30:19.738Z
---

memorable v1 is single-user: stdio MCP transport, Postgres in Docker on 127.0.0.1 (password auth, app password in OS keyring), Ollama on localhost. Colleagues run their own instance in v1 (README must include setup instructions). Sharing a server between colleagues (HTTP transport, per-user auth, visibility rules, personal-context policy) is v2. Decided 2026-11-14.

**Why:** User explicitly deferred the shared-server questions (transport, visibility, personal context on shared servers) to v2.

**How to apply:** Do not design or ask about multi-user features for v1. Keep v2 cheap: owner column on all tables, transport behind an interface, embedding model/dimension stored per row.
