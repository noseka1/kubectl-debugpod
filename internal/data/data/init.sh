#!/bin/sh

# Uncomment for troubleshooting
#set -o xtrace

cri_provider="$1"
cri_id="$2"
cri_socket="$3"
shift 3
hostfs=/host
chroot $hostfs \
  crictl \
    --runtime-endpoint "$cri_socket" \
    info \
  1>/dev/null
if [ $? -ne 0 ]; then
  echo Failed to communicate with container runtime using socket $cri_socket
  exit 1
fi
indent=$(
  chroot $hostfs \
    crictl \
      --runtime-endpoint "$cri_socket" \
      inspect \
      --output yaml \
     "$cri_id" \
  | sed --quiet --expression '/ \+/p' \
  | sed --quiet --expression '1s/^\( \+\).*/\1/p'
)
pid=$(
  chroot $hostfs \
    crictl \
      --runtime-endpoint "$cri_socket" \
      inspect \
      --output yaml \
      "$cri_id" \
  | sed --quiet --expression "/^${indent}pid:.*/s/${indent}pid: \([[:digit:]]\+\)/\1/p"
)
if [ -z "$pid" ]; then
  # assume dockershim runtime, cri id equals to docker container id
  pid=$(chroot $hostfs docker inspect --format '{{.State.Pid}}' "$cri_id")
fi
if [ -z "$pid" ]; then
  echo Failed to find the process PID for pod with cri_id=$cri_id
  exit 1
fi

mkdir -p /kubectl-debugpod
cat >/kubectl-debugpod/print_root.sh <<EOF
#!/bin/sh
hostfs=$hostfs
cri_id=$cri_id
cri_provider=$cri_provider
if [ \$cri_provider = docker ]; then
  root=\$(chroot \$hostfs docker inspect --format '{{.GraphDriver.Data.MergedDir}}' "\$cri_id")
elif [ \$cri_provider = containerd ]; then
  root=/run/containerd/io.containerd.runtime.v1.linux/k8s.io/\$cri_id/rootfs
elif [ \$cri_provider = cri-o ]; then
  root=\$(chroot \$hostfs runc state \$cri_id | sed --quiet --expression 's/ \+"rootfs": "\(.*\)".*/\1/p')
fi
if [ -z "\$root" -o ! -d "\$hostfs\$root" ]; then
  echo Failed to obtain the root directory
  exit 1
fi
echo \$hostfs\$root
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
  --target $pid \
  /bin/sh -c 'mount -t proc proc /proc || true; exec "$@"' /bin/sh "${exec_args[@]}"
