# Contributing

vitvm is Go with no external dependencies, so the binary drops in anywhere.

- `go test ./...` must pass.
- The pieces are deliberately small and separately tested: `internal/cas` (the
  blob store), `internal/tree` (snapshots and diff), `internal/snap` (restore),
  `internal/engine` (the checkpoint chain, operations, and remote push/pull),
  `internal/remote` (S3 and directory object stores),
  `internal/fcvm` (the Firecracker microVM backend), `internal/agent` (the
  host/guest protocol), `cmd/vit-guest` (the in-guest agent and init).
- A new backend implements `engine.Backend`. The store, the chain, and the CLI
  should not need to change; if they do, that is a design smell worth raising.
- Be exact about what a backend captures. The process backend checkpoints
  files; the firecracker backend checkpoints files, memory, state and disk.
- Engine changes that touch machines should keep passing against both the fake
  machine (`internal/engine`) and the Firecracker stand-in (`internal/fcvm`).

The firecracker backend that adds memory is described in `docs/firecracker.md`.
