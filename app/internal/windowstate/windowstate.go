// Package windowstate persists the desktop window geometry between launches.
package windowstate

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"autoipo/internal/envfile"
)

// Default and minimum window dimensions (Fyne content size in points).
const (
	DefaultWidth  float32 = 1100
	DefaultHeight float32 = 720
	MinWidth      float32 = 820
	MinHeight     float32 = 560
)

// State is the persisted window geometry.
type State struct {
	Width  float32 `json:"width"`
	Height float32 `json:"height"`
}

// Default returns the geometry used on first launch.
func Default() State { return State{Width: DefaultWidth, Height: DefaultHeight} }

// Clamped returns the state with invalid or tiny dimensions replaced so the
// window is always usable.
func (s State) Clamped() State {
	s.Width = clampDimension(s.Width, DefaultWidth, MinWidth)
	s.Height = clampDimension(s.Height, DefaultHeight, MinHeight)
	return s
}

func clampDimension(value, fallback, minimum float32) float32 {
	if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) || value <= 0 {
		return fallback
	}
	if value < minimum {
		return minimum
	}
	return value
}

// Load reads the state at path. A missing or unreadable file yields Default.
func Load(path string) State {
	data, err := os.ReadFile(path)
	if err != nil {
		return Default()
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return Default()
	}
	return state.Clamped()
}

// Save atomically writes the clamped state to path, creating the directory
// with owner-only permissions when needed.
func Save(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state.Clamped(), "", "  ")
	if err != nil {
		return err
	}
	return envfile.WriteFileAtomic(path, append(data, '\n'), 0o600)
}
