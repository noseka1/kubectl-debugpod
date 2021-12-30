#!/bin/sh

set -o xtrace

CRI_PROVIDER="$1"
CRI_ID="$2"
CRI_SOCKET="$3"
shift 3
HOSTFS=/host
chroot $HOSTFS \
  crictl \
    --runtime-endpoint "$CRI_SOCKET" \
	info
if [ $? -ne 0 ]; then
  echo Failed to communicate with container runtime using socket $CRI_SOCKET
  exit 1
fi
INDENT=$(
  chroot $HOSTFS \
    crictl \
      --runtime-endpoint "$CRI_SOCKET" \
	  inspect \
      --output yaml \
     "$CRI_ID" \
  | sed --quiet --expression '/ \+/p' \
  | sed --quiet --expression '1s/^\( \+\).*/\1/p'
)
PID=$(
  chroot $HOSTFS \
    crictl \
      --runtime-endpoint "$CRI_SOCKET" \
	  inspect \
      --output yaml \
      "$CRI_ID" \
  | sed --quiet --expression "/^${INDENT}pid:.*/s/${INDENT}pid: \([[:digit:]]\+\)/\1/p"
)
if [ -z "$PID" ]; then
  # assume dockershim runtime, cri id equals to docker container id
  PID=$(chroot $HOSTFS docker inspect --format '{{.State.Pid}}' "$CRI_ID")
fi
if [ -z "$PID" ]; then
  echo Failed to find the process PID for pod with CRI_ID=$CRI_ID
  exit 1
fi

mkdir -p /kubectl-debugpod
cat >/kubectl-debugpod/print_root.sh <<EOF
#!/bin/sh
HOSTFS=$HOSTFS
CRI_ID=$CRI_ID
CRI_PROVIDER=$CRI_PROVIDER
if [ \$CRI_PROVIDER = docker ]; then
  ROOT=\$(chroot \$HOSTFS docker inspect --format '{{.GraphDriver.Data.MergedDir}}' "\$CRI_ID")
elif [ \$CRI_PROVIDER = containerd ]; then
  ROOT=/run/containerd/io.containerd.runtime.v1.linux/k8s.io/\$CRI_ID/rootfs
elif [ \$CRI_PROVIDER = cri-o ]; then
  ROOT=\$(chroot \$HOSTFS runc state \$CRI_ID | sed --quiet --expression 's/ \+"rootfs": "\(.*\)".*/\1/p')
fi
if [ -z "\$ROOT" -o ! -d "\$HOSTFS\$ROOT" ]; then
  echo Failed to obtain the root directory
  exit 1
fi
echo \$HOSTFS\$ROOT
EOF
chmod 755 /kubectl-debugpod/print_root.sh

# mount the target's container filesystem
mkdir -p /target
mount --bind $(/kubectl-debugpod/print_root.sh) /target || true

declare -a exec_args
exec_args=("$@")

exec nsenter \
  --uts \
  --ipc \
  --net \
  --pid \
  --cgroup \
  --no-fork \
  --target $PID \
  /bin/sh -c 'mount -t proc proc /proc || true; exec "$@"' /bin/sh "${exec_args[@]}"
