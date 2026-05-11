package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var validNotesFormats = []string{"markdown", "plain", "json"}

func newReleaseNotesCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
	)

	cmd := &cobra.Command{
		Use:          "release-notes <app-name>",
		Short:        "Generate release notes for a specific app",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			_ = args[0]
			_ = currentTag
			_ = previousTag

			// TODO: implement full release notes generation
			fmt.Fprintln(cmd.ErrOrStderr(), "release-notes: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")

	return cmd
}

func newReleaseNotesAllCmd() *cobra.Command {
	var (
		currentTag  string
		previousTag string
		formatType  string
		outputDir   string
	)

	cmd := &cobra.Command{
		Use:          "release-notes-all",
		Short:        "Generate release notes for all apps",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isValidNotesFormat(formatType) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: markdown, plain, json\n")
				return fmt.Errorf("invalid format")
			}

			_ = currentTag
			_ = previousTag
			_ = outputDir

			// TODO: implement full release notes generation
			fmt.Fprintln(cmd.ErrOrStderr(), "release-notes-all: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().StringVar(&currentTag, "current-tag", "HEAD", "Current tag/version")
	cmd.Flags().StringVar(&previousTag, "previous-tag", "", "Previous tag to compare against")
	cmd.Flags().StringVar(&formatType, "format", "markdown", "Output format (markdown, plain, json)")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory to save release notes files")

	return cmd
}

func isValidNotesFormat(f string) bool {
	for _, v := range validNotesFormats {
		if f == v {
			return true
		}
	}
	return false
}
