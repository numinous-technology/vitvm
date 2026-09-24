#!/usr/bin/env bash
set -euo pipefail
work=$(mktemp -d)
cd "$work"

vit init
vit new cobol

vit run -- sh -c 'echo "IDENTIFICATION DIVISION." > prog.cbl'
vit run -- sh -c 'echo "  PROGRAM-ID. hello." >> prog.cbl'
vit run -- sh -c 'echo "this line is the bug" >> prog.cbl'

echo
echo "history:"
vit log

good=$(vit log | grep 'step 2' | grep -o 'ck-[0-9a-f]*')
echo
echo "prog.cbl as it was at the good step ($good), without re-running:"
vit show "$good" prog.cbl

echo
echo "forking from the good step and taking a different next line:"
vit fork "$good" cobol-fixed
vit run -- sh -c 'echo "  DISPLAY \"hello\"." >> prog.cbl'
vit run -- sh -c 'echo "  STOP RUN." >> prog.cbl'

echo
echo "the fixed sandbox:"
vit log
echo
vit ls
