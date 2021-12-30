package cmd

import (
	"github.com/spf13/cobra"
)

func RootCmd() *cobra.Command {

	params := DebugCmdParams{}

	var rootCmd = &cobra.Command{
		Use:                   "kubectl-debugpod POD [flags] [-- COMMAND [args...]]",
		Short:                 "Attach troubleshooting tools to running Kubernetes/OpenShift pods",
		Long:                  `kubectl-debugpod, complete documentation is available at https://github.com/noseka1/kubectl-debugpod`,
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
	return rootCmd
}
