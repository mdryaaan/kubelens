package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/mdryaaan/kubelens/pkg/version"
)

var versionAsJSON bool

func newVersionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version, commit, and build metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := version.Current()

			if versionAsJSON {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(info)
			}

			fmt.Fprintln(cmd.OutOrStdout(), info.String())
			return nil
		},
	}

	cmd.Flags().BoolVar(&versionAsJSON, "json", false, "print the metadata as JSON")
	return cmd
}
