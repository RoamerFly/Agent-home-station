#!/bin/sh
# Creates a new disposable root. Does not alter the host mount namespace.
set -eu
test "$(id -u)" = 0
task_root=/mnt/e/A-Importance/wechat-bot/research
oracle_root=$(mktemp -d /tmp/wechat-rqtx-oracle.XXXXXX)
chmod 755 "$oracle_root"
mkdir -p "$oracle_root/lib/x86_64-linux-gnu" "$oracle_root/lib64"
gcc -O2 -Wall -Wextra -o "$oracle_root/oracle" "$task_root/scripts/rqtx_oracle.c" -ldl
for task_lib in libc.so.6 libm.so.6 libstdc++.so.6 libgcc_s.so.1; do
 cp -L "/lib/x86_64-linux-gnu/$task_lib" "$oracle_root/lib/x86_64-linux-gnu/$task_lib"
done
cp -L /lib64/ld-linux-x86-64.so.2 "$oracle_root/lib64/ld-linux-x86-64.so.2"
cp "$task_root/samples/libv08.so.bin" "$oracle_root/libv08.so"
cp "$task_root/samples/rqtx.dat.bin" "$oracle_root/rqtx.dat"
chmod -R a-w "$oracle_root"
printf 'Read-only isolated oracle root: %s\n' "$oracle_root" >&2
# A fresh mount/PID/network namespace, a read-only bind mount, no host directories
# mounted in the chroot, UID/GID nobody, no children or networking by seccomp.
export oracle_root
unshare --mount --net --pid --fork sh -c '
 set -eu
 mount --make-rprivate /
 mount --bind "$oracle_root" "$oracle_root"
 mount -o remount,bind,ro "$oracle_root"
 exec timeout 15 chroot --userspec=65534:65534 "$oracle_root" /oracle "$@"
' sh "$@"
