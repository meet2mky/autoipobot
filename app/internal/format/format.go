// Package format renders values for the desktop app's labels: masked
// identifiers, relative ages and durations.
package format

import (
	"fmt"
	"strings"
	"time"
)

const bullet = "•"

// MaskPAN hides the middle of a PAN: "ABCDE1234F" becomes "ABCDE••••F".
// Shorter or malformed values keep only their last character.
func MaskPAN(pan string) string {
	pan = strings.ToUpper(strings.TrimSpace(pan))
	runes := []rune(pan)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) == 10:
		return string(runes[:5]) + strings.Repeat(bullet, 4) + string(runes[9:])
	default:
		return strings.Repeat(bullet, len(runes)-1) + string(runes[len(runes)-1:])
	}
}

// MaskUserID keeps the first two characters of a login ID.
func MaskUserID(userID string) string {
	runes := []rune(strings.TrimSpace(userID))
	if len(runes) == 0 {
		return ""
	}
	if len(runes) <= 2 {
		return strings.Repeat(bullet, len(runes))
	}
	return string(runes[:2]) + strings.Repeat(bullet, 4)
}

// Age renders how long ago t happened relative to now ("never" when zero).
func Age(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	elapsed := now.Sub(t)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds ago", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh %dm ago", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
}

// Duration renders an elapsed time compactly: "45s", "5m 03s", "2h 05m",
// "3d 02h".
func Duration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %02dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// Timestamp renders a local wall-clock time, or "—" when zero.
func Timestamp(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}
