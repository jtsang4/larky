package macos

import (
	"errors"
	"os"
	"strings"
)

type State struct {
	DisplayAsleep bool   `json:"display_asleep"`
	ScreenLocked  bool   `json:"screen_locked"`
	Away          bool   `json:"away"`
	Method        string `json:"method"`
}

type Detector interface {
	Detect() (State, error)
}

type SystemDetector struct{}

func (SystemDetector) Detect() (State, error) {
	if strings.TrimSpace(os.Getenv("LARKY_TEST_MODE")) == "1" {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("LARKY_AWAY_OVERRIDE"))) {
		case "away", "1", "true":
			return State{DisplayAsleep: true, Away: true, Method: "test-override"}, nil
		case "present", "0", "false":
			return State{Away: false, Method: "test-override"}, nil
		case "error":
			return State{}, errors.New("synthetic away detector failure")
		}
	}
	return detectSystemState()
}
