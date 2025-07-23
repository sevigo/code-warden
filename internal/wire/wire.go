//go:build wireinject
// +build wireinject

package wire

import (
	"context"

	"github.com/google/wire"
	"github.com/sevigo/code-warden/internal/app"
)

func InitializeApp(ctx context.Context) (*app.App, func(), error) {
	wire.Build(AppSet)
	return &app.App{}, nil, nil
}
