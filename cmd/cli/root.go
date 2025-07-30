package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "warden-cli",
	Short: "warden-cli is the command-line interface for Code-Warden.",
	Long:  `A CLI for managing and interacting with the Code-Warden service, allowing for administrative tasks like preloading repositories.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() { //nolint:gochecknoinits // Cobra's init function for command registration
	rootCmd.AddCommand(scanCmd)
}
