package main

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "warden-cli",
	Short: "warden-cli is a CLI tool for Code Warden",
	Long:  `A command-line interface for interacting with the Code Warden application.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.AddCommand(preloadCmd)
}
