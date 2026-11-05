# Changelog

## 0.4.0

- Rename the project, Go module, plugin/provider ID, management routes, browser storage and build artifacts to `cpa-agentrouter-continuity`.
- Publish source and Linux amd64 shared-library distributions under the project repository.
- Preserve model discovery with preselected existing models, runtime-only API keys, default Codex request headers, session-origin protection and the two-active-session limit per AR key.
- Document migration of private credentials and origin state from the earlier plugin identity. Do not enable both scheduler plugins together.

This release changes identifiers; it does not automatically migrate or restart an existing CPA service.
