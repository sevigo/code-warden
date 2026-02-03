// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v73/github"
	"golang.org/x/oauth2"

	"github.com/sevigo/code-warden/internal/config"
)

// CreateInstallationClient creates a GitHub client that is authenticated as a specific application installation.
// It will now return the client, the raw token string, and an error.
func CreateInstallationClient(ctx context.Context, cfg *config.Config, installationID int64, logger *slog.Logger) (Client, string, error) {
	logger.Info("Creating GitHub installation client", "installation_id", installationID)

	// Load private key
	privateKey, err := os.ReadFile(cfg.GitHub.PrivateKeyPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read private key from %s: %w", cfg.GitHub.PrivateKeyPath, err)
	}

	// Create JWT source
	// We use the apps transport to interact with the GitHub App API (e.g. to get installation tokens)
	appTransport, err := ghinstallation.NewAppsTransport(http.DefaultTransport, cfg.GitHub.AppID, privateKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create GitHub App transport: %w", err)
	}
	appClient := github.NewClient(&http.Client{Transport: appTransport})

	// Get the installation token
	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create installation token for installation ID %d: %w", installationID, err)
	}
	if token.GetToken() == "" {
		return nil, "", fmt.Errorf("received an empty installation token")
	}
	logger.Info("Successfully created installation token", "installation_id", installationID, "expires_at", token.GetExpiresAt())

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.GetToken()})
	tc := oauth2.NewClient(ctx, ts)
	installationClient := github.NewClient(tc)

	return NewGitHubClient(installationClient, logger), token.GetToken(), nil
}
