package windowstate

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileReturnsDefault(t *testing.T) {
	state := Load(filepath.Join(t.TempDir(), "window.json"))
	if state != Default() {
		t.Fatalf("expected default state, got %+v", state)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "window.json")
	want := State{Width: 1280, Height: 800}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600 permissions, got %v", info.Mode().Perm())
	}
	if got := Load(path); got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}

func TestClampedEnforcesMinimumsAndDefaults(t *testing.T) {
	cases := []struct {
		in, want State
	}{
		{State{Width: 100, Height: 100}, State{Width: MinWidth, Height: MinHeight}},
		{State{Width: 0, Height: -5}, Default()},
		{State{Width: float32(math.NaN()), Height: float32(math.Inf(1))}, Default()},
		{State{Width: 1600, Height: 900}, State{Width: 1600, Height: 900}},
	}
	for _, tc := range cases {
		if got := tc.in.Clamped(); got != tc.want {
			t.Fatalf("Clamped(%+v) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

func TestLoadCorruptFileReturnsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if state := Load(path); state != Default() {
		t.Fatalf("expected default state for corrupt file, got %+v", state)
	}
}
