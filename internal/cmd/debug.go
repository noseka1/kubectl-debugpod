package cmd

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"

	"kubectl-debugpod/internal/data"

	"github.com/spf13/viper"
	corev1 "k8s.io/api/core/v1"
	kapierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubectl/pkg/cmd/attach"
	kcmdutil "k8s.io/kubectl/pkg/cmd/util"
	"k8s.io/kubectl/pkg/util/interrupt"
	"k8s.io/utils/pointer"
)

var containerRuntimes = map[string]string{
	"docker":     "unix:///var/run/dockershim.sock",
	"containerd": "unix:///var/run/containerd/containerd.sock",
	"cri-o":      "unix:///var/run/crio/crio.sock",
}

type DebugCmdParams struct {
	pod           string
	namespace     string
	container     string
	initContainer bool
	image         string
}

type DebugCmd struct {
	params     DebugCmdParams
	initScript string
}

func NewDebugCmd(params DebugCmdParams) *DebugCmd {
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

func (cmd *DebugCmd) generateDebugPodName(pod string) string {
	rand.Seed(time.Now().UnixNano())
	chars := []rune("abcdefghijklmnopqrstuvwxyz" + "0123456789")
	length := 5
	var suffix strings.Builder
	for i := 0; i < length; i++ {
		suffix.WriteRune(chars[rand.Intn(len(chars))])
	}
	return pod + "-debug-" + suffix.String()
}

func (cmd *DebugCmd) prepareDebugPodManifest(node string, podName string, criProvider string, criID string, criSocket string, image string) *corev1.Pod {
	pod := &corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: cmd.generateDebugPodName(podName),
		},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{
				{
					Name:    "debug",
					Image:   image,
					Command: []string{"/bin/sh", "-c", cmd.initScript, "/bin/sh", criProvider, criID, criSocket},
					TTY:     true,
					Stdin:   true,
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

func (cmd *DebugCmd) attachToPod(kubeClient kubernetes.Interface, pod *corev1.Pod) error {
	kubeConfigFlags := genericclioptions.NewConfigFlags(true)
	matchVersionKubeConfigFlags := kcmdutil.NewMatchVersionFlags(kubeConfigFlags)
	f := kcmdutil.NewFactory(matchVersionKubeConfigFlags)
	config, err := f.ToRESTConfig()
	if err != nil {
		return err
	}
	ioStreams := genericclioptions.IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
	attachOptions := attach.NewAttachOptions(ioStreams)
	attachOptions.Config = config
	attachOptions.Pod = pod
	attachOptions.TTY = true
	attachOptions.Stdin = true
	attachOptions.InterruptParent = interrupt.New(func(os.Signal) { os.Exit(1) }, func() {
		log.Printf("Removing debug pod ...")
		err := kubeClient.CoreV1().Pods(pod.Namespace).Delete(context.TODO(), pod.Name, *metav1.NewDeleteOptions(0))
		if err != nil {
			if !kapierrors.IsNotFound(err) {
				log.Printf("Unable to delete the debug pod %s: %v", pod.Name, err)
			}
		}
	})
	return attachOptions.InterruptParent.Run(func() error {
		return attachOptions.Run()
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

	debugPodManifest := cmd.prepareDebugPodManifest(pod.Spec.NodeName, pod.Name, criProvider, criID, criSocket, cmd.params.image)

	log.Printf("Starting pod %s on node %s using image %s ...", debugPodManifest.ObjectMeta.Name, pod.Spec.NodeName, cmd.params.image)

	debugPod, err := kubeClient.CoreV1().Pods(currentNamespace).Create(context.TODO(), debugPodManifest, metav1.CreateOptions{})
	if err != nil {
		log.Fatal(err)
	}

	debugPod, err = cmd.waitForPodStart(kubeClient, debugPod)
	if err != nil {
		log.Fatal(err)
	}

	log.Print("To use host binaries, run 'nsenter --mount --target 1'")

	err = cmd.attachToPod(kubeClient, debugPod)
	if err != nil {
		log.Fatal(err)
	}
}
