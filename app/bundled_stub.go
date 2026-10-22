//go:build !bundled

package main

// bundledHelpersAvailable reports whether this binary embeds the helpers.
// Without -tags bundled the app relies on whatever deploy.sh already put in
// <root>/bin.
const bundledHelpersAvailable = false

// installBundledHelpers is a no-op when the helpers are not embedded.
func installBundledHelpers(string) error { return nil }
