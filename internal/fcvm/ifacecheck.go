package fcvm

import "github.com/numinous-technology/vitvm/internal/engine"

var _ engine.MemoryBackend = (*Firecracker)(nil)
