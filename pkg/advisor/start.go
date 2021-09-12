package advisor

import (
	"flag"
	"fmt"
	"os"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

func init() {
	flag.Set("logtostderr", "true")
	// hack to make flag.Parsed return true such that glog is happy
	// about the flags having been parsed
	flag.CommandLine.Parse([]string{})
}

// Execute will execute basically the whole application
func Execute() {
	options := &Options{}
	flag.Lookup("logtostderr").Value.Set("true")
	glog.Infof("Starting application...\n")
	glog.Flush()
	rootCmd := &cobra.Command{
		Use:   "resource-advisor",
		Short: "Kubernetes resource-advisor",
		Long:  "Kubernetes resource-advisor",
		Run: func(cmd *cobra.Command, args []string) {
			err := Run(options)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\n%v\n", err)
				os.Exit(1)
				return
			}
		},
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
