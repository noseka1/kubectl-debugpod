# kubectl-debugpod

*kubectl-debugpod* tool allows you to attach troubleshooting tools to running Kubernetes and/or OpenShift pods.

## How does it work?

The process of troubleshooting a running pod using *kubectl-debugpod* can be divided into the following steps:

1. *kubectl-debugpod* creates a privileged pod using a user-supplied container image. This debug pod is placed on the same cluster node where the target pod is running.
2. User's terminal is attached to the debug pod.
3. The shell running in the debug pod joins the Linux namespaces (*uts*, *ipc*, *net*, *pid*, and *cgroup*) of the target container.
4. User runs troubleshooting tools located on her/his container image, enjoying direct access to the processes running in the target container.
5. After the user is finished with troubleshooting, the debug pod is deleted.

![Diagram](docs/kubectl_debugpod_diagram.svg "Diagram")

## Building kubectl-debugpod (optional)

Golang >= 1.15 is required. Build *kubectl-debugpod* using the command:

```
$ make build
```
## Installing kubectl-debugpod

Download the release archive from [GitHub](https://github.com/noseka1/kubectl-debugpod/releases) and extract it. Copy the `kube-debugpod` binary to your $PATH, for example:

```
$ cp kubectl-debugpod /usr/local/bin
```

## Usage

Check out the built-in help:

```
$ kubectl-debugpod -h
kubectl-debugpod, complete documentation is available at https://github.com/noseka1/kubectl-debugpod

Usage:
  kubectl-debugpod POD [flags] [-- COMMAND [args...]]

Examples:
  # Run an interactive shell session in the target pod
  kubectl-debugpod mypod -i -t

  # List contents of / from the target container.
  kubectl-debugpod mypod -- ls /proc/1/root

Flags:
  -c, --container string          Target container name; defaults to first container in pod
      --forward-address strings   Addresses to listen on (comma separated). Only accepts IP addresses or localhost as a value. When localhost is supplied, kubectl-debugpod will try to bind on both 127.0.0.1 and ::1 and will fail if neither of these addresses are available to bind. (default [localhost])
      --forward-port strings      Forward one or more local ports to the target pod. Comma-separated list of port mappings.
  -h, --help                      help for kubectl-debugpod
      --image string              Image used by the debug pod.
      --init-container            Target is an init container; defaults to false
  -n, --namespace string          Target namespace
  -i, --stdin                     Pass stdin to the container
  -t, --tty                       Stdin is a TTY
```

The following sample session demonstrates attaching to an `apiserver-c45b4454d-vlfmf` pod that is running in the `openshift-apiserver` namespace:

```
$ kubectl-debugpod -ti -n openshift-apiserver apiserver-c45b4454d-vlfmf -c openshift-apiserver
2022/01/04 09:31:10 Starting pod apiserver-c45b4454d-vlfmf-debug-cfxi7 on node ip-10-0-139-43.us-east-2.compute.internal using image docker.io/centos:latest ...
2022/01/04 09:31:11 Filesystem of the target container is accessible at /proc/1/root. You can also inspect this file system using 'nsenter --mount --target 1'
sh-4.4# ps aux
USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND
root           1  2.8  1.2 1693800 203632 ?      Ssl  Jan03  43:56 openshift-apiserver start --config=/var/run/configmaps/config/config.yaml -v=2
root          35  0.0  0.0  12052  3244 pts/1    S    08:31   0:00 /bin/sh
root          37  0.0  0.0  44668  3480 pts/1    R+   08:31   0:00 ps aux
sh-4.4# exit
exit
2022/01/04 09:31:23 Removing debug pod ...
```

There are two mounts accessible from within the running *kubectl-debugpod*:

* `/host` The root directory of the underlying Kubernetes node is mounted here.
* `/proc/1/root` The root directory of the target container is accesible through here.

## Configuration file

*kubectl-debugpod* can read the configuration from a file located at `~/.kubectl-debugpod.yaml`. Sample configuration file:
```
image: quay.io/noseka1/toolbox-container:basic
```

## Tools image requirements

Your tools image can contain debugging/troubleshooting tools of your choice. In addition to that, make sure that you include the following utilities in your image as well. These utilities are used by *kube-debud-pod* to initialize the environment within the container:

* `/bin/sh`
* `chroot`
* `nsenter`
* `mount`
* `sed`
* `sleep`

There are many tools images available out there. If you are troubleshooting on OpenShift, you can checkout my [toolbox-container](https://github.com/noseka1/toolbox-container) tools image.

## Limitations

* User must be permitted to create privileged containers.
* The following container runtimes are supported:
  * CRI-O
  * containerd
  * dockershim
  
  No further container runtimes are supported at this time.

## References

Kubernetes feature [Ephemeral Containers](https://github.com/kubernetes/enhancements/issues/277) that is currently in development.

Similar projects:

* [aylei/kubectl-debug](https://github.com/aylei/kubectl-debug)
* [verb/kubectl-debug](https://github.com/verb/kubectl-debug)
* [huazhihao/kubespy](https://github.com/huazhihao/kubespy)
