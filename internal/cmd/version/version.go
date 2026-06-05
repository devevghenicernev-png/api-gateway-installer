// Package version implements `apigw version` — prints build info.
//
// Supports --json for scripting. Works even when the config is broken or
// missing — that's why Factory.Config is lazy.
package version

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/devevghenicernev-png/apigw/internal/cmdutil"
	"github.com/spf13/cobra"
)

type info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

func NewCmdVersion(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version info",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			i := info{
				Version:   f.AppVersion,
				Commit:    f.Commit,
				BuildDate: f.BuildDate,
				GoVersion: runtime.Version(),
				OS:        runtime.GOOS,
				Arch:      runtime.GOARCH,
			}
			asJSON, _ := c.Flags().GetBool("json")
			if asJSON {
				b, err := json.MarshalIndent(i, "", "  ")
				if err != nil {
					return err
				}
				fmt.Fprintln(f.IOStreams.Out, string(b))
				return nil
			}
			fmt.Fprintf(f.IOStreams.Out, "apigw %s\n", i.Version)
			fmt.Fprintf(f.IOStreams.Out, "  commit: %s\n", i.Commit)
			fmt.Fprintf(f.IOStreams.Out, "  built:  %s\n", i.BuildDate)
			fmt.Fprintf(f.IOStreams.Out, "  go:     %s\n", i.GoVersion)
			fmt.Fprintf(f.IOStreams.Out, "  os:     %s/%s\n", i.OS, i.Arch)
			return nil
		},
	}
	return cmd
}
