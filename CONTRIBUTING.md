# Contributing

vitvm is Go with no external dependencies, so the binary drops in anywhere.

- `go test ./...` must pass.
- The pieces are deliberately small and separately tested: `internal/cas` (the
  blob store), `internal/tree` (snapshots and diff), `internal/snap` (restore),
  `internal/engine` (the checkpoint chain, operations, and remote push/pull),
  `internal/remote` (S3 and directory object stores).
- A new backend implements `engine.Backend`. The store, the chain, and the CLI
  should not need to change; if they do, that is a design smell worth raising.
- Be exact about what a backend captures. The process backend checkpoints
  files; do not describe it as capturing memory.

The firecracker backend that adds memory is described in `docs/firecracker.md`.
