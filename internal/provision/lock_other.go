//go:build !linux && !darwin && !windows

package provision

import (
	"context"
	"errors"
)

func lock(context.Context, string) (func(), error) {
	return nil, errors.New("Wok browser installation supports Linux and macOS")
}
