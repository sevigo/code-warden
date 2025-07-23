package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	githubToken string
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
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().StringVarP(&githubToken, "github-token", "t", "", "GitHub Token")

	if err := viper.BindPFlag("GITHUB_TOKEN", rootCmd.PersistentFlags().Lookup("github-token")); err != nil {
		slog.Error("Error binding flag", "error", err)
		os.Exit(1)
	}
}

// initConfig reads in config file and ENV variables if set.
func initConfig() {
	viper.SetEnvPrefix("CW")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
}
