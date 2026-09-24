#!/bin/bash
# vitvm end to end on a KVM host, through the vit CLI. Run as root.
set -u -o pipefail
cd /root
PASS=0; FAIL=0
check(){ if eval "$2"; then echo "PASS  $1"; PASS=$((PASS+1)); else echo "FAIL  $1   [$2]"; FAIL=$((FAIL+1)); fi; }
ck(){ vit log ${2:-} | grep " step $1 " | grep -o 'ck-[0-9a-f]*' | head -1; }
timed(){ local s=$(date +%s%N); "$@"; T=$(( ($(date +%s%N)-s)/1000000 )); }
echo "##### host: $(uname -r), $(nproc) cpus, /dev/kvm $(test -c /dev/kvm && echo present)"
apt-get update -q >/dev/null 2>&1; apt-get install -y -q squashfs-tools >/dev/null 2>&1
echo "##### scripts/fetch-firecracker.sh"
bash vitvm/scripts/fetch-firecracker.sh /opt/fc 2>&1 | tail -2 || { echo "SETUP FAILED: fetch"; exit 1; }
echo "##### scripts/build-rootfs.sh"
VIT_GUEST=/root/vit-guest bash vitvm/scripts/build-rootfs.sh /opt/fc/base.squashfs /opt/fc/rootfs.ext4 1024 2>&1 | tail -1
[ -s /opt/fc/rootfs.ext4 ] || { echo "SETUP FAILED: rootfs"; exit 1; }
install -m 0755 /root/vit /usr/local/bin/vit
mkdir -p demo && cd demo
vit init >/dev/null
vit config backend firecracker; vit config firecracker.bin /opt/fc/firecracker
vit config firecracker.kernel /opt/fc/vmlinux; vit config firecracker.rootfs /opt/fc/rootfs.ext4
vit config firecracker.mem_mib 512
echo "##### a sandbox, steps"
vit new agent
timed vit run -- sh -c 'uname -r; echo hello > notes.txt' > /tmp/o1; cat /tmp/o1; echo "  step 1 took ${T} ms (boot + step + checkpoint)"
check "commands run in the guest kernel, not the host's" "grep -q '^6\.1\.' /tmp/o1 && ! grep -q \"$(uname -r)\" /tmp/o1"
vit run -- sh -c 'nohup sh -c "i=0; while :; do i=\$((i+1)); echo \$i > /dev/shm/count; sleep 0.2; done" >/dev/null 2>&1 & echo counter started' | head -1
sleep 3
timed vit run -- cat /dev/shm/count > /tmp/o3; N1=$(head -1 /tmp/o3); echo "  step 3 read the counter: $N1 (step took ${T} ms)"
sleep 5
vit run -- sh -c 'cat /dev/shm/count; echo more >> notes.txt' > /tmp/o4; N2=$(head -1 /tmp/o4); echo "  step 4 read the counter: $N2"
check "a background process keeps running between steps" "[ '$N2' -gt '$N1' ]"
vit log
S3=$(ck 3); S4=$(ck 4)
check "every firecracker step carries a machine image" "[ \$(vit log | grep -c '+machine') -ge 4 ]"
echo "##### show and diff read checkpoints without a machine"
vit show $S3 notes.txt > /tmp/show
check "show step 3's notes.txt" "[ \"\$(cat /tmp/show)\" = hello ]"
vit diff $S3 $S4 > /tmp/diff; cat /tmp/diff
check "diff step 3 to 4 shows the modified file" "grep -q '~ notes.txt' /tmp/diff"
echo "##### warm fork from step 3"
timed vit fork $S3 retry; echo "  fork took ${T} ms"
vit run -- sh -c 'cat /dev/shm/count; sleep 1; cat /dev/shm/count; cat notes.txt' > /tmp/of; cat /tmp/of
F1=$(sed -n 1p /tmp/of); F2=$(sed -n 2p /tmp/of)
check "the fork's counter resumed from step 3, not from zero and not from step 4" "[ '$F1' -ge '$N1' ] && [ '$F1' -lt '$N2' ]"
check "the fork's background process is alive and counting" "[ '$F2' -gt '$F1' ]"
check "the fork's disk is step 3's (no 'more')" "[ \"\$(sed -n 3p /tmp/of)\" = hello ] && ! grep -qx more /tmp/of"
echo "##### a fresh sandbox has no such process"
vit new fresh >/dev/null
vit run -- sh -c 'cat /dev/shm/count 2>/dev/null || echo none' > /tmp/ofresh
check "a cold sandbox has no counter" "[ \"\$(head -1 /tmp/ofresh)\" = none ]"
echo "##### checkout rewinds the machine"
vit use agent >/dev/null
vit run -- cat /dev/shm/count > /tmp/obefore; B=$(head -1 /tmp/obefore)
vit checkout $S3
vit run -- sh -c 'cat /dev/shm/count; cat notes.txt' > /tmp/oco; C=$(sed -n 1p /tmp/oco); cat /tmp/oco
check "after checkout the counter is back near step 3 (was $B)" "[ '$C' -ge '$N1' ] && [ '$C' -lt '$N2' ] && [ '$C' -lt '$B' ]"
check "after checkout the disk is step 3's" "[ \"\$(sed -n 2p /tmp/oco)\" = hello ] && ! grep -qx more /tmp/oco"
echo "##### stop, then run resumes from the head"
vit run -- cat /dev/shm/count > /tmp/ohead; H=$(head -1 /tmp/ohead)
vit stop agent
vit ls | grep agent
check "stop shuts the machine down" "vit ls | grep -q 'agent.*stopped'"
vit run -- sh -c 'cat /dev/shm/count; sleep 1; cat /dev/shm/count' > /tmp/ores; R1=$(sed -n 1p /tmp/ores); R2=$(sed -n 2p /tmp/ores)
check "run after stop resumed the head's machine (counter $R1 >= $H, still counting)" "[ '$R1' -ge '$H' ] && [ '$R2' -gt '$R1' ]"
echo "##### push, then pull and warm-fork on another repo"
vit push agent --to /root/remote
mkdir -p /root/repo2 && vit init /root/repo2 >/dev/null
export VIT_DIR=/root/repo2/.vit VIT_BACKEND=firecracker VIT_FIRECRACKER_BIN=/opt/fc/firecracker VIT_FIRECRACKER_KERNEL=/opt/fc/vmlinux VIT_FIRECRACKER_ROOTFS=/opt/fc/rootfs.ext4 VIT_FIRECRACKER_MEM_MIB=512
vit pull $S3 --from /root/remote --as pulled
vit run -- sh -c 'cat /dev/shm/count; cat notes.txt' > /tmp/opull; P=$(sed -n 1p /tmp/opull); cat /tmp/opull
check "a pulled checkpoint forks warm on another repo" "[ '$P' -ge '$N1' ] && [ '$P' -lt '$N2' ] && [ \"\$(sed -n 2p /tmp/opull)\" = hello ] && ! grep -qx more /tmp/opull"
unset VIT_DIR VIT_BACKEND
echo "##### storage"
M=$(cat /root/demo/.vit/checkpoints/*.json | grep -c '"mem_hash"')
LOGICAL=$(( M * (512 + 1024) ))
USED=$(( $(du -sm /root/demo/.vit/blobs | cut -f1) ))
echo "  $M machine checkpoints; stored whole they would be $LOGICAL MiB; the repo holds $USED MiB"
echo "  guest console check: $(grep -h 'overlay root' /tmp/vit-fc/*/fc.log 2>/dev/null | head -1)"
check "chunked images dedupe (repo < 25% of whole images)" "[ $USED -lt $((LOGICAL/4)) ]"
echo "##### speed"
cd /root/demo
steps(){ # $1 sandbox name; five small steps; prints each step's ms and the median
  vit new $1 >/dev/null; vit run -- true >/dev/null
  B0=$(du -sm /root/demo/.vit/blobs | cut -f1); TS=""
  for i in 1 2 3 4 5; do timed vit run -- sh -c "echo $i >> f; head -c 100000 /dev/urandom > r$i" >/dev/null; TS="$TS $T"; done
  B1=$(du -sm /root/demo/.vit/blobs | cut -f1)
  MED=$(echo $TS | tr ' ' '\n' | sort -n | sed -n 3p)
  echo "  $1: step ms:$TS  median $MED; storage +$(( (B1-B0)/5 )) MiB per step"
}
steps perf
PERF_MED=$MED
S=$(ck 3 perf)
timed vit fork $S f1 >/dev/null; FORK1=$T
vit use perf >/dev/null
timed vit fork $S f2 >/dev/null; FORK2=$T
echo "  fork: first ${FORK1} ms (image reassembled), second ${FORK2} ms (from the cache)"
vit run -- cat f | head -3 | tr '\n' ' '; echo "(the fork's f)"
check "a small step takes under 1.5 s" "[ $PERF_MED -lt 1500 ]"
check "a fork from the cache takes under 1 s" "[ $FORK2 -lt 1000 ]"
VIT_FIRECRACKER_DISK_MODE=copy steps perfcopy
echo "  (perfcopy: the old full-disk mode, for comparison)"
for s in $(vit ls | grep -o 'sbx-[0-9a-f]*'); do VIT_FIRECRACKER_DISK_MODE=copy vit stop $s >/dev/null 2>&1; vit stop $s >/dev/null 2>&1; done
VIT_DIR=/root/repo2/.vit vit stop pulled >/dev/null 2>&1
echo "##### result: $PASS passed, $FAIL failed"
