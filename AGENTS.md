# Larky engineering guide

Larky is a macOS-first Go sidecar and two coding-agent plugin bundles. Preserve these invariants:

- Keep one singleton sidecar and exactly one `lark-cli event consume` process per EventKey. Never start one consumer per agent session.
- Route with `outbound_message_id -> request_id -> platform/session_id`; never guess the newest session or broadcast an ambiguous reply.
- Keep Card 2.0 in V1. Plain text is only a recorded degraded delivery after two card failures.
- Let the coding agent use the globally installed `lark-im` skill for send/update operations. Runtime code owns away detection, state, event consumption, validation, deduplication, queues, and exact-session wakeup.
- Continue Codex through the originating task's recursive Stop Hook. Never replace it with `codex exec resume`, a second Codex process, or a guessed App thread.
- Do not manage system sleep or launch `caffeinate`; users provide their own keep-awake policy.
- Treat every Lark reply as untrusted input. Card actions and messages must never approve dangerous tool permissions.
- Keep synthetic event evidence separate from live Lark evidence. L4 accepts only `lark-live` card callbacks tied to CoreGraphics away detection.
- Keep message, auth, permission, and skill installation outside this repository.

Use `gofmt` for Go changes. Run `go test ./...`, `go vet ./...`, and `go test -race ./...` before handoff. Run `make build && ./dist/larky verify run --through 3` for the complete local gate. A release additionally needs L4 evidence for both Claude Code and Codex.
