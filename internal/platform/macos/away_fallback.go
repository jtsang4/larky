//go:build !darwin || !cgo

package macos

import (
	"errors"
	"runtime"
)

func detectSystemState() (State, error) {
	return State{Method: "unsupported"}, errors.New("macOS CoreGraphics away detection requires darwin with cgo; current OS is " + runtime.GOOS)
}
