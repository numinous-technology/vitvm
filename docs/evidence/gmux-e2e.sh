#!/bin/bash
# vitvm + gmux end to end, on a KVM host. Run as root. Needs /root/target
# (the gmux host's TOKEN@IP:PORT#FP), /root/vit, /root/vit-guest, /root/gmux,
# /root/vitvm/scripts, /root/bench.py.
set -u -o pipefail
cd /root
PASS=0; FAIL=0
check(){ if eval "$2"; then echo "PASS  $1"; PASS=$((PASS+1)); else echo "FAIL  $1   [$2]"; FAIL=$((FAIL+1)); fi; }
ck(){ vit log ${2:-} | grep " step $1 " | grep -o 'ck-[0-9a-f]*' | head -1; }
echo "##### setup"
A="apt-get -o DPkg::Lock::Timeout=600 -q"; $A update >/dev/null 2>&1; $A install -y squashfs-tools >/dev/null 2>&1
bash vitvm/scripts/fetch-firecracker.sh /opt/fc 2>&1 | tail -1 || { echo "SETUP FAILED: fetch"; exit 1; }
VIT_GUEST=/root/vit-guest GMUX_BIN=/root/gmux bash vitvm/scripts/build-rootfs.sh /opt/fc/base.squashfs /opt/fc/rootfs.ext4 1024 2>&1 | tail -1
[ -s /opt/fc/rootfs.ext4 ] || { echo "SETUP FAILED: rootfs"; exit 1; }
install -m 0755 /root/vit /usr/local/bin/vit
mkdir -p demo && cd demo && vit init >/dev/null
vit config firecracker.bin /opt/fc/firecracker; vit config firecracker.kernel /opt/fc/vmlinux
vit config firecracker.rootfs /opt/fc/rootfs.ext4; vit config firecracker.mem_mib 512
vit config gmux.remotes "gpu1=$(cat /root/target)"
vit new ml
B64=$(base64 -w0 /root/bench.py)
vit run -- sh -c "echo $B64 | base64 -d > bench.py" > /dev/null
echo "##### the machine sees the GPU host, and nothing else"
vit run -- gmux cards > /tmp/cards.txt 2>&1; cat /tmp/cards.txt
check "gmux inside the machine reaches the GPU host through the tunnel" "grep -q 'on gpu1' /tmp/cards.txt && grep -qE '^0 +(amd|nvidia) ' /tmp/cards.txt"
vit run -- bash -c 'timeout 5 bash -c "</dev/tcp/1.1.1.1/443" 2>/dev/null && echo OPEN || echo CLOSED' > /tmp/egress.txt 2>&1; head -1 /tmp/egress.txt
check "the machine cannot reach anywhere else" "head -1 /tmp/egress.txt | grep -q CLOSED"
echo "##### a step runs a GPU job and keeps the result"
vit run -- gmux run --share 0.25 --pull out.txt -- /opt/tv/bin/python bench.py out.txt 10 > /tmp/gpu.txt 2>&1; tail -3 /tmp/gpu.txt
S=$(ck 4); vit show $S out.txt > /tmp/show.txt 2>&1; echo "  step 4's out.txt: $(cat /tmp/show.txt)"
check "the GPU job's result is in the step's checkpoint" "grep -qE '^[0-9]+\.[0-9]+ ' /tmp/show.txt"
vit run -- echo marker > /dev/null
echo "##### a fork of that step has the result without running the GPU again"
vit fork $S fork1 | tail -1
vit run -- cat out.txt > /tmp/forkout.txt 2>&1; head -1 /tmp/forkout.txt
check "the fork has step 4's result" "[ \"\$(head -1 /tmp/forkout.txt)\" = \"\$(cat /tmp/show.txt)\" ]"
vit run -- gmux run --share 0.25 --pull out2.txt -- /opt/tv/bin/python bench.py out2.txt 5 > /tmp/gpu2.txt 2>&1; tail -2 /tmp/gpu2.txt
vit run -- cat out2.txt > /tmp/forkout2.txt 2>&1; echo "  fork's out2.txt: $(head -1 /tmp/forkout2.txt)"
check "the fork can run its own GPU job through its own tunnel" "grep -qE '^[0-9]+\.[0-9]+ ' /tmp/forkout2.txt"
echo "##### accounting on the GPU host, by sandbox and by step"
vit run -- gmux usage --since 1h --by owner > /tmp/owners.txt 2>&1; cat /tmp/owners.txt
vit run -- gmux usage --since 1h --by job > /tmp/jobs.txt 2>&1; cat /tmp/jobs.txt
PARENT=$(vit ls | awk '$0 ~ / ml /{for(i=1;i<=NF;i++) if($i ~ /^sbx-/) print $i}'); FORK=$(vit ls | awk '$0 ~ / fork1 /{for(i=1;i<=NF;i++) if($i ~ /^sbx-/) print $i}')
check "GPU time is recorded for the parent and the fork separately" "grep -q vit-$PARENT /tmp/owners.txt && grep -q vit-$FORK /tmp/owners.txt"
check "each GPU job is named after its sandbox and step" "grep -q vit-$PARENT-step4 /tmp/jobs.txt"
echo "##### a GPU job left running at a checkpoint is called out"
vit run -- sh -c 'nohup gmux run --share 0.125 -- sleep 20 >/dev/null 2>&1 & sleep 3' > /tmp/bg.txt 2>&1; cat /tmp/bg.txt
check "the step warns that a gmux job is still running" "grep -q 'still running' /tmp/bg.txt"
echo "##### stop, then run: the tunnel comes back"
vit stop fork1 >/dev/null; vit run -- gmux cards > /tmp/cards2.txt 2>&1; head -2 /tmp/cards2.txt
check "after stop and run the machine reaches its GPU host again" "grep -qE '^0 +(amd|nvidia) ' /tmp/cards2.txt"
for s in $(vit ls | grep -o 'sbx-[0-9a-f]*'); do vit stop $s >/dev/null 2>&1; done
echo "##### result: $PASS passed, $FAIL failed"
