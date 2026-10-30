# Server-side quota history (option 3, timer polling)

## Feature playbook
- [x] 1. `how` over the affected subsystem. (done inline: dashboard quotaHistory/quotaPoolModel/recorder, server api_tools APICall, quota signals, v8 management routes)
- [x] 2. `architect` for parallel design exploration. skip: source chosen by user (timer polling); contract is narrow and fixed by the dashboard's existing per-(account, window) history ids; machine allows at most two subagents at once
- [x] 3. Write the throughput checkpoint as four todo items.
  - Blocking first steps: API contract (below), written before fan-out.
  - Independent workstreams: server (Go, CLIProxyAPI-quota-history) and dashboard (TS, Cli-Proxy-API-Management-Center-quota-history). Disjoint repos.
  - Shared mutable state: only the JSON contract. Fixed up front, so no serialization.
  - Smallest safe decomposition: two workers, one per repo.
- [x] 4. Delegate code-writing to a subagent (two delegates, own worktrees, no commits without user authorization)
- [x] 5. Verify on the matching surface (server endpoint with real credentials; dashboard chart)
- [x] 6. Rebase into small, ordered commits. (user authorized merge and run; ff-merged to local main, not pushed)
- [x] 7. If the design is contested, `interrogate` before shipping. skip: not contested; source chosen by user
- [ ] 8. Run **Opening a PR**. skip: user asked to merge locally and run, not to push or open PRs

## Contract
GET /v8/management/credentials/quota-history
{ "interval_seconds": 900,
  "accounts": [ { "name": "<auth file name>", "provider": "claude"|"codex",
                  "windows": { "<window id>": [[atMs, remainingPercent], ...] } } ] }
Window ids match the dashboard: claude five-hour, seven-day; codex five-hour, weekly, monthly.
