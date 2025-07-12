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

// CreateInstallationClient creates a GitHub client that is authenticated as a specific
// application installation. This is the primary way the application interacts with the
// GitHub API on behalf of a user or organization that has installed the app.
//
// The authentication process involves two main steps:
//  1. Authenticate as the GitHub App itself using a JSON Web Token (JWT).
//  2. Use the app-level authentication to request a temporary, installation-specific
//     OAuth token.
func CreateInstallationClient(ctx context.Context, cfg *config.Config, installationID int64, logger *slog.Logger) (Client, error) {
	logger.Info("Creating GitHub installation client", "installation_id", installationID)

	// The private key is used to sign the JWT, proving the request is from our app.
	privateKeyBytes, err := os.ReadFile(cfg.GitHubPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read GitHub private key file from '%s': %w", cfg.GitHubPrivateKeyPath, err)
	}

	// This token identifies the application itself and is valid for a short period (10 minutes).
	jwtToken, err := createJWT(cfg.GitHubAppID, privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create JWT for GitHub App: %w", err)
	}

	// This client is authenticated using the JWT and is only used to get an installation token.
	appClient := github.NewClient(nil).WithAuthToken(jwtToken)

	// We ask the GitHub API to grant us a token that is scoped to the specific installation.
	token, _, err := appClient.Apps.CreateInstallationToken(ctx, installationID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create installation token for installation ID %d: %w", installationID, err)
	}
	if token.GetToken() == "" {
		return nil, fmt.Errorf("received an empty installation token")
	}
	logger.Info("Successfully created installation token", "installation_id", installationID, "expires_at", token.GetExpiresAt())

	// This client is authenticated with the installation token and is what the application
	// will use to perform all its operations (e.g., commenting on PRs, updating checks).
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token.GetToken()})
	tc := oauth2.NewClient(ctx, ts)
	installationClient := github.NewClient(tc)

	return NewGitHubClient(installationClient, logger), nil
}

// createJWT generates a JSON Web Token (JWT) used to authenticate as a GitHub App.
// The token is signed with the app's private RSA key and includes the app's ID as the issuer.
// It has a short expiration time (10 minutes) as required by GitHub.
func createJWT(appID int64, privateKeyBytes []byte) (string, error) {
	if appID == 0 || len(privateKeyBytes) == 0 {
		return "", fmt.Errorf("app ID and private key must be provided")
	}

	// The claims identify our application and set the token's validity period.
	claims := &jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(10 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", appID),
	}

	// Parse the PEM-encoded private key.
	key, err := jwt.ParseRSAPrivateKeyFromPEM(privateKeyBytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse RSA private key: %w", err)
	}

	// Create and sign the token using the RS256 algorithm.
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signedString, err := token.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	// slog.Debug("Successfully created and signed JWT token", "app_id", appID)
	return signedString, nil
}
