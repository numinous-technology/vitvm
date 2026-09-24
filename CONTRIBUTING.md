# Contributing

vitvm is Go with no external dependencies, so the binary drops in anywhere.

- `go test ./...` must pass.
- The pieces are deliberately small and separately tested: `internal/cas` (the
  blob store), `internal/tree` (snapshots and diff), `internal/snap` (restore),
  `internal/engine` (the checkpoint chain and operations).
- A new backend implements `engine.Backend`. The store, the chain, and the CLI
  should not need to change; if they do, that is a design smell worth raising.
- Keep the CLI honest about what a backend captures. The process backend
  checkpoints files; do not describe it as capturing memory.

The firecracker backend that adds memory is described in `docs/firecracker.md`.
