package engine

import (
	"fmt"
	"os"
	"syscall"
)

// diskUsage is the space a file really takes; sparse images take less than
// their size.
func diskUsage(path string, fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return fi.Size()
}

// fileIdentity names a file's current contents cheaply: a rewrite changes its
// size or modification time, a replacement changes its inode.
func fileIdentity(path string, fi os.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%d:%d", st.Dev, st.Ino, fi.Size(), fi.ModTime().UnixNano())
	}
	return fmt.Sprintf("%s:%d:%d", path, fi.Size(), fi.ModTime().UnixNano())
}
