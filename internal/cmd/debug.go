package cmd

import (
	"context"
	"fmt"
	"io"
	"kubectl-debugpod/internal/data"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/transport/spdy"

	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/kubectl/pkg/cmd/exec"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/polymorphichelpers"
	"k8s.io/kubectl/pkg/util/interrupt"
	psaapi "k8s.io/pod-security-admission/api"
	"k8s.io/utils/pointer"
)

var containerRuntimes = map[string]string{
	"docker":     "unix:///var/run/dockershim.sock",
	"containerd": "unix:///var/run/containerd/containerd.sock",
	"cri-o":      "unix:///var/run/crio/crio.sock",
}

type DebugCmdParams struct {
	pod            string
	namespace      string
	container      string
	initContainer  bool
	image          string
	stdin          bool
	tty            bool
	forwardAddress []string
	forwardPort    []string
}

type DebugCmd struct {
	params     DebugCmdParams
	command    []string
	initScript string
}

func NewDebugCmd(params DebugCmdParams, command []string) *DebugCmd {
	file, err := data.Assets.Open("init.sh")
	if err != nil {
		log.Fatalf("Failed to open the init script. %s", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Failed to read the init script. %s", err)
	}
	return &DebugCmd{
		params:     params,
		command:    command,
		initScript: string(data),
	}
}

func (cmd *DebugCmd) kubeConfig() clientcmd.ClientConfig {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
}

func (cmd *DebugCmd) kubeClient(kubeConfig clientcmd.ClientConfig) (kubernetes.Interface, error) {
	clientConfig, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig failed: %w", err)
	}

	kubeClient, err := kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client from config: %w", err)
	}
	return kubeClient, nil
}

func (cmd *DebugCmd) findExistingContainer(pod *corev1.Pod) (string, bool, error) {
	var containerNames []string
	for _, c := range pod.Spec.Containers {
		containerNames = append(containerNames, c.Name)
	}
	var initContainerNames []string
	for _, c := range pod.Spec.InitContainers {
		initContainerNames = append(initContainerNames, c.Name)
	}
	container := cmd.params.container
	initContainer := cmd.params.initContainer
	if initContainer {
		if container == "" {
			if len(initContainerNames) > 1 {
				return "", false, fmt.Errorf("an init container name must be specified for pod %s, choose one of: %s", cmd.params.pod, initContainerNames)
			}
			container = initContainerNames[0]
		} else if !stringInSlice(cmd.params.container, initContainerNames) {
			return "", false, fmt.Errorf("init container %s not found in pod %s, choose one of: %s", container, cmd.params.pod, initContainerNames)
		}
	} else {
		if container == "" {
			if len(containerNames) > 1 {
				return "", false, fmt.Errorf("a container name must be specified for pod %s, choose one of: %s or one of the init containers: %s", cmd.params.pod, containerNames, initContainerNames)
			}
			container = containerNames[0]
		} else if !stringInSlice(container, containerNames) {
			if stringInSlice(container, initContainerNames) {
				initContainer = true
			} else {
				return "", false, fmt.Errorf("container %s not found in pod %s, choose one of: %s or one of the init containers: %s", container, cmd.params.pod, containerNames, initContainerNames)
			}
		}
	}
	return container, initContainer, nil
}

func (cmd *DebugCmd) findContainerID(pod *corev1.Pod, container string, initContainer bool) (string, error) {
	searchList := pod.Status.ContainerStatuses
	if initContainer {
		searchList = pod.Status.InitContainerStatuses
	}

	for _, status := range searchList {
		if status.Name == container {
			return status.ContainerID, nil
		}
	}
	return "", fmt.Errorf("cannot find containerID for container %s (initContainer=%t)", container, initContainer)
}

func (cmd *DebugCmd) parseContainerID(containerID string) (string, string, string, error) {
	separator := "://"
	containerIDSlice := strings.SplitN(containerID, separator, 2)
	if len(containerIDSlice) == 2 {
		cri := containerIDSlice[0]
		socket, found := containerRuntimes[cri]
		if found {
			return containerIDSlice[0], containerIDSlice[1], socket, nil
		}
		return "", "", "", fmt.Errorf("unsupported container runtime: %s", containerID)
	}
	return "", "", "", fmt.Errorf("failed to parse containerID %s", containerID)
}

func (cmd *DebugCmd) prepareTemporaryNamespaceManifest() *corev1.Namespace {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "kubectl-debugpod-",
			Labels: map[string]string{
				psaapi.EnforceLevelLabel:                         string(psaapi.LevelPrivileged),
				psaapi.AuditLevelLabel:                           string(psaapi.LevelPrivileged),
				psaapi.WarnLevelLabel:                            string(psaapi.LevelPrivileged),
				"security.openshift.io/scc.podSecurityLabelSync": "false",
			},
		},
	}
	return ns
}

func (cmd *DebugCmd) prepareDebugPodManifest(node string, podName string, image string) *corev1.Pod {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: podName + "-debug-",
		},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{
				{
					Name:    "debug",
					Image:   image,
					Command: []string{"/bin/sh", "-c", "trap : TERM INT; sleep infinity & wait"},
					TTY:     cmd.params.tty,
					Stdin:   cmd.params.stdin,
					SecurityContext: &corev1.SecurityContext{
						Privileged: pointer.BoolPtr(true),
					},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "host",
						MountPath: "/host",
					}},
				},
			},
			HostNetwork:   true,
			HostPID:       true,
			HostIPC:       true,
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes: []corev1.Volume{{
				Name:         "host",
				VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}},
			}},
		},
	}
	return pod
}

func (cmd *DebugCmd) waitForPodStart(kubeClient kubernetes.Interface, pod *corev1.Pod) (*corev1.Pod, error) {
	watch, err := kubeClient.CoreV1().Pods(pod.ObjectMeta.Namespace).Watch(context.TODO(), metav1.ListOptions{
		Watch:           true,
		ResourceVersion: pod.ObjectMeta.ResourceVersion,
		FieldSelector:   "metadata.name=" + pod.ObjectMeta.Name,
	})
	if err != nil {
		return nil, err
	}
	func() {
		for {
			select {
			case events := <-watch.ResultChan():
				if events.Object == nil {
					watch.Stop()
					return
				}
				pod = events.Object.(*corev1.Pod)
				if pod.Status.Phase == corev1.PodRunning {
					watch.Stop()
					return
				}
			case <-time.After(15 * time.Minute):
				watch.Stop()
				return
			}
		}
	}()
	if pod.Status.Phase != corev1.PodRunning {
		return pod, fmt.Errorf("pod %s failed to reach state running", pod.ObjectMeta.Name)
	}
	return pod, nil
}

func (cmd *DebugCmd) forwardPortToPod(pod *corev1.Pod) error {
	if len(cmd.params.forwardPort) == 0 {
		return nil
	}

	kubeConfigFlags := genericclioptions.NewConfigFlags(true)
	matchVersionKubeConfigFlags := kcmdutil.NewMatchVersionFlags(kubeConfigFlags)
	f := kcmdutil.NewFactory(matchVersionKubeConfigFlags)
	config, err := f.ToRESTConfig()
	if err != nil {
		return err
	}

	restClient, err := f.RESTClient()
	if err != nil {
		return err
	}

	req := restClient.Post().
		Resource("pods").
		Namespace(pod.GetNamespace()).
		Name(pod.GetName()).
		SubResource("portforward")

	transport, upgrader, err := spdy.RoundTripperFor(config)
	if err != nil {
		return err
	}
	stopChannel := make(chan struct{})
	readyChannel := make(chan struct{})
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", req.URL())
	fw, err := portforward.NewOnAddresses(dialer, cmd.params.forwardAddress, cmd.params.forwardPort, stopChannel, readyChannel, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	go fw.ForwardPorts()
	return nil
}

func (cmd *DebugCmd) attachToPod(kubeClient kubernetes.Interface, pod *corev1.Pod, criProvider string, criID string, criSocket string) error {
	kubeConfigFlags := genericclioptions.NewConfigFlags(true)
	matchVersionKubeConfigFlags := kcmdutil.NewMatchVersionFlags(kubeConfigFlags)
	f := kcmdutil.NewFactory(matchVersionKubeConfigFlags)
	config, err := f.ToRESTConfig()
	if err != nil {
		return err
	}
	command := append([]string{"/bin/sh", "-c", cmd.initScript, "/bin/sh", criProvider, criID, criSocket}, cmd.command...)
	execOptions := &exec.ExecOptions{
		StreamOptions: exec.StreamOptions{
			Namespace: pod.GetNamespace(),
			IOStreams: genericclioptions.IOStreams{
				In:     os.Stdin,
				Out:    os.Stdout,
				ErrOut: os.Stderr,
			},
			TTY:   cmd.params.tty,
			Stdin: cmd.params.stdin,
		},
		ResourceName:    pod.GetName(),
		Command:         command,
		Builder:         f.NewBuilder,
		ExecutablePodFn: polymorphichelpers.AttachablePodForObjectFn,
		Executor:        &exec.DefaultRemoteExecutor{},
		Config:          config,
	}
	execOptions.InterruptParent = interrupt.New(func(os.Signal) { os.Exit(1) }, func() {
		log.Printf("Removing debug pod ...")
		err := kubeClient.CoreV1().Pods(pod.Namespace).Delete(context.TODO(), pod.Name, *metav1.NewDeleteOptions(0))
		if err != nil {
			if !kapierrors.IsNotFound(err) {
				log.Printf("Unable to delete the debug pod %s: %v", pod.Name, err)
			}
		}
		err = kubeClient.CoreV1().Namespaces().Delete(context.TODO(), pod.Namespace, metav1.DeleteOptions{})
		if err != nil {
			if !kapierrors.IsNotFound(err) {
				log.Printf("Unable to delete the temporary namespace %s: %v", pod.Namespace, err)
			}

		}
	})
	return execOptions.InterruptParent.Run(func() error {
		return execOptions.Run()
	})
}

func (cmd *DebugCmd) readConfiguration() error {

	viper.SetConfigName(".kubectl-debugpod")
	viper.SetConfigType("yaml")

	tmp, exists := os.LookupEnv("HOME")
	if exists && tmp != "" {
		viper.AddConfigPath(tmp)
	}
	viper.AddConfigPath(".")

	var configuration Configuration

	// Read the config file if it exists
	err := viper.ReadInConfig()
	if err == nil {
		err = viper.Unmarshal(&configuration)
		if err != nil {
			return err
		}
		if cmd.params.image == "" {
			if configuration.Image != "" {
				cmd.params.image = configuration.Image
			}
		}
	}
	if cmd.params.image == "" {
		cmd.params.image = "docker.io/centos:latest"
	}
	return nil
}

func (cmd *DebugCmd) Execute() {

	err := cmd.readConfiguration()
	if err != nil {
		log.Fatalf("Failed to read the configuration. %s", err)
	}

	kubeConfig := cmd.kubeConfig()

	kubeClient, err := cmd.kubeClient(kubeConfig)
	if err != nil {
		log.Fatal(err)
	}

	currentNamespace, _, err := kubeConfig.Namespace()
	if err != nil {
		log.Fatalf("Couldn't get kubeconfig namespace. %s", err)
	}

	namespace := cmd.params.namespace
	if namespace == "" {
		namespace = currentNamespace
	}

	pod, err := kubeClient.CoreV1().Pods(namespace).Get(context.TODO(), cmd.params.pod, metav1.GetOptions{})
	if err != nil {
		log.Fatalf("Failed to find pod %s in namespace %s: %s", cmd.params.pod, namespace, err)
	}

	containerName, initContainer, err := cmd.findExistingContainer(pod)
	if err != nil {
		log.Fatal(err)
	}

	containerID, err := cmd.findContainerID(pod, containerName, initContainer)
	if err != nil {
		log.Fatal(err)
	}

	criProvider, criID, criSocket, err := cmd.parseContainerID(containerID)
	if err != nil {
		log.Fatal(err)
	}

	tmpNamespaceManifest := cmd.prepareTemporaryNamespaceManifest()

	tmpNamespace, err := kubeClient.CoreV1().Namespaces().Create(context.TODO(), tmpNamespaceManifest, metav1.CreateOptions{})
	if err != nil {
		log.Fatal(err)
	}

	debugPodManifest := cmd.prepareDebugPodManifest(pod.Spec.NodeName, pod.Name, cmd.params.image)

	log.Printf("Starting pod on node %s using image %s ...", pod.Spec.NodeName, cmd.params.image)

	debugPod, err := kubeClient.CoreV1().Pods(tmpNamespace.Name).Create(context.TODO(), debugPodManifest, metav1.CreateOptions{})
	if err != nil {
		log.Fatal(err)
	}

	debugPod, err = cmd.waitForPodStart(kubeClient, debugPod)
	if err != nil {
		log.Fatal(err)
	}

	log.Print("Filesystem of the target container is accessible at /proc/1/root. " +
		"You can also inspect this file system using 'nsenter --mount --target 1'")

	err = cmd.forwardPortToPod(pod)
	if err != nil {
		log.Fatal(err)
	}

	err = cmd.attachToPod(kubeClient, debugPod, criProvider, criID, criSocket)
	if err != nil {
		log.Fatal(err)
	}
}
