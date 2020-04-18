package cmd

import (
	"github.com/spf13/cobra"
)

func RootCmd() *cobra.Command {

	params := DebugCmdParams{}

	var rootCmd = &cobra.Command{
		Use:   "kube-debug-pod POD",
		Short: "Attach troubleshooting tools to running Kubernetes/OpenShift pods",
		Long:  `Complete documentation is available at https://github.com/noseka1/kube-debug-pod`,
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			params.pod = args[0]
			NewDebugCmd(params).Execute()
		},
	}
	rootCmd.PersistentFlags().StringVarP(&params.namespace, "namespace", "n", "", "Debug pod in namespace")
	rootCmd.PersistentFlags().StringVarP(&params.container, "container", "c", "", "Debug container name; defaults to first container in pod")
	rootCmd.PersistentFlags().BoolVarP(&params.initContainer, "init-container", "", false, "Debug an init container; defaults to false")
	rootCmd.PersistentFlags().StringVarP(&params.image, "image", "", "centos", "Image used by the debug pod.")
	return rootCmd
}
