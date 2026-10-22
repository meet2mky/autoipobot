package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// Accent colours shared by the theme and the flow canvas.
var (
	accentPrimary   = color.NRGBA{R: 99, G: 102, B: 241, A: 255}
	accentFocus     = color.NRGBA{R: 45, G: 212, B: 191, A: 255}
	accentSelection = color.NRGBA{R: 99, G: 102, B: 241, A: 96}
	buttonDark      = color.NRGBA{R: 24, G: 35, B: 52, A: 255}
	buttonLight     = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
)

// appTheme applies autoipo's accent colours and defers every other colour,
// font, icon and size to the default theme so the system light/dark variant
// is respected.
type appTheme struct {
	base fyne.Theme
}

func newAppTheme() fyne.Theme { return &appTheme{base: theme.DefaultTheme()} }

// Color implements fyne.Theme.
func (t *appTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	case theme.ColorNamePrimary:
		return accentPrimary
	case theme.ColorNameFocus:
		return accentFocus
	case theme.ColorNameSelection:
		return accentSelection
	case theme.ColorNameButton:
		if variant == theme.VariantLight {
			return buttonLight
		}
		return buttonDark
	default:
		return t.base.Color(name, variant)
	}
}

// Font implements fyne.Theme.
func (t *appTheme) Font(style fyne.TextStyle) fyne.Resource { return t.base.Font(style) }

// Icon implements fyne.Theme.
func (t *appTheme) Icon(name fyne.ThemeIconName) fyne.Resource { return t.base.Icon(name) }

// Size implements fyne.Theme.
func (t *appTheme) Size(name fyne.ThemeSizeName) float32 { return t.base.Size(name) }
