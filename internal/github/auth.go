// Package github provides functionality for interacting with the GitHub API.
package github

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v73/github"
	"golang.org/x/oauth2"

	"github.com/sevigo/code-warden/internal/config"
)

// CreateInstallationClient creates a GitHub client that is authenticated as a specific application installation.
func CreateInstallationClient(ctx context.Context, cfg *config.Config, installationID int64, logger *slog.Logger) (Client, error) {
	logger.Info("Creating GitHub installation client", "installation_id", installationID)

	privateKeyBytes, err := os.ReadFile(cfg.GitHubPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub private key file from '%s': %w", cfg.GitHubPrivateKeyPath, err)
	}

	jwtToken, err := createJWT(cfg.GitHubAppID, privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT for GitHub App: %w", err)
	}

	appClient := github.NewClient(nil).WithAuthToken(jwtToken)

	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create installation token for installation ID %d: %w", installationID, err)
	}
	if token.GetToken() == "" {
		return nil, fmt.Errorf("received an empty installation token")
	}
	logger.Info("Successfully created installation token", "installation_id", installationID, "expires_at", token.GetExpiresAt())

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.GetToken()})
	tc := oauth2.NewClient(ctx, ts)
	installationClient := github.NewClient(tc)

	return NewGitHubClient(installationClient, logger), nil
}

// createJWT generates a JSON Web Token (JWT) used to authenticate as a GitHub App.
func createJWT(appID int64, privateKeyBytes []byte) (string, error) {
	if appID == 0 || len(privateKeyBytes) == 0 {
		return "", fmt.Errorf("app ID and private key must be provided")
	}

	now := time.Now()
	issuedAt := now.Add(-30 * time.Second)
	expiresAt := now.Add(9 * time.Minute)

	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(issuedAt),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		Issuer:    fmt.Sprintf("%d", appID),
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse RSA private key: %w", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedString, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return signedString, nil
}
