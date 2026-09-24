# Fork from a past step

A three-step run where step three introduces a bug. Instead of rerunning from
scratch, we read the good step, fork from it, and take a different path.

Needs `vit` on your PATH (`go build -o /usr/local/bin/vit ./cmd/vit`).

    ./demo.sh
