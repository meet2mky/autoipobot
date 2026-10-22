package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// setEnabled toggles a disableable widget.
func setEnabled(w fyne.Disableable, enabled bool) {
	if enabled {
		w.Enable()
	} else {
		w.Disable()
	}
}

// tintButton colours a button by the outcome of its last action.
func tintButton(button *widget.Button, ok bool) {
	if ok {
		button.Importance = widget.SuccessImportance
	} else {
		button.Importance = widget.DangerImportance
	}
	button.Refresh()
}

// resetButton restores the default button appearance.
func resetButton(button *widget.Button) {
	button.Importance = widget.MediumImportance
	button.Refresh()
}

// boldLabel returns a bold, left-aligned label.
func boldLabel(text string) *widget.Label {
	return widget.NewLabelWithStyle(text, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
}

// wrappedLabel returns a word-wrapping label for longer explanatory text.
func wrappedLabel(text string) *widget.Label {
	label := widget.NewLabel(text)
	label.Wrapping = fyne.TextWrapWord
	return label
}

// textPane is a read-only, selectable, monospace text area inside a scroll
// container, used for logs and command output.
type textPane struct {
	label  *widget.Label
	scroll *container.Scroll
}

func newTextPane(placeholder string) *textPane {
	label := widget.NewLabel(placeholder)
	label.TextStyle = fyne.TextStyle{Monospace: true}
	label.Wrapping = fyne.TextWrapWord
	label.Selectable = true
	scroll := container.NewScroll(label)
	scroll.SetMinSize(fyne.NewSize(200, 120))
	return &textPane{label: label, scroll: scroll}
}

// setText replaces the content, optionally keeping the view pinned to the end.
func (p *textPane) setText(text string, follow bool) {
	p.label.SetText(text)
	if follow {
		p.scroll.Refresh() // pick up the new content size before scrolling
		p.scroll.ScrollToBottom()
	}
}

// fieldRow stacks a caption above a widget; the caption can be hidden with
// the widget by hiding the returned container.
func fieldRow(caption string, w fyne.CanvasObject) *fyne.Container {
	return container.NewVBox(boldLabel(caption), w)
}

// hintEntry returns a single-line entry with placeholder text.
func hintEntry(placeholder string) *widget.Entry {
	entry := widget.NewEntry()
	entry.SetPlaceHolder(placeholder)
	return entry
}
