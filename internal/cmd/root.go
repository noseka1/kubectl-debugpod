package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/kubectl/pkg/util/templates"
)

func RootCmd() *cobra.Command {

	usageExample := templates.Examples(`
		# Run an interactive shell session in the target pod
		kubectl-debugpod mypod -i -t

		# List contents of / from the target container.
		kubectl-debugpod mypod -- ls /proc/1/root
		`)

	params := DebugCmdParams{}

	var rootCmd = &cobra.Command{
		Use:                   "kubectl-debugpod POD [flags] [-- COMMAND [args...]]",
		Short:                 "Attach troubleshooting tools to running Kubernetes/OpenShift pods",
		Long:                  `kubectl-debugpod, complete documentation is available at https://github.com/noseka1/kubectl-debugpod`,
		Example:               usageExample,
		Args:                  cobra.MinimumNArgs(1),
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			params.pod = args[0]
			argsLenAtDash := cmd.ArgsLenAtDash()
			var command []string
			if argsLenAtDash > -1 {
				command = args[argsLenAtDash:]
			} else {
				command = []string{"/bin/sh"}
			}
			NewDebugCmd(params, command).Execute()
		},
	}
	rootCmd.PersistentFlags().StringVarP(&params.namespace, "namespace", "n", "", "Target namespace")
	rootCmd.PersistentFlags().StringVarP(&params.container, "container", "c", "", "Target container name; defaults to first container in pod")
	rootCmd.PersistentFlags().BoolVarP(&params.initContainer, "init-container", "", false, "Target is an init container; defaults to false")
	rootCmd.PersistentFlags().StringVarP(&params.image, "image", "", "", "Image used by the debug pod.")
	rootCmd.Flags().BoolVarP(&params.stdin, "stdin", "i", params.stdin, "Pass stdin to the container")
	rootCmd.Flags().BoolVarP(&params.tty, "tty", "t", params.tty, "Stdin is a TTY")
	rootCmd.Flags().StringSliceVar(&params.forwardAddress, "forward-address", []string{"localhost"}, "Addresses to listen on (comma separated). Only accepts IP addresses or localhost as a value. When localhost is supplied, kubectl-debugpod will try to bind on both 127.0.0.1 and ::1 and will fail if neither of these addresses are available to bind.")
	rootCmd.Flags().StringSliceVar(&params.forwardPort, "forward-port", []string{}, "Forward one or more local ports to the target pod. Comma separated list of port mappings.")
	return rootCmd
}
