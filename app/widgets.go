package main

import (
	"strings"

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

// textPane is a read-only, selectable, monospace text area inside a
// vertical scroll container, used for logs and command output. The scroll is
// vertical-only on purpose: a two-axis scroll gives a wrapping label unbounded
// width, so long lines would never wrap and a horizontal scrollbar would
// appear and fight the mouse wheel.
type textPane struct {
	label  *widget.Label
	scroll *container.Scroll
	text   string
}

// textPaneMaxLines bounds what a pane displays; the backing logs keep more,
// but laying out thousands of wrapped lines makes every resize stutter.
const textPaneMaxLines = 500

func newTextPane(placeholder string) *textPane {
	label := widget.NewLabel(placeholder)
	label.TextStyle = fyne.TextStyle{Monospace: true}
	label.Wrapping = fyne.TextWrapWord
	label.Selectable = true
	scroll := container.NewVScroll(label)
	scroll.SetMinSize(fyne.NewSize(200, 120))
	return &textPane{label: label, scroll: scroll, text: placeholder}
}

// setText replaces the content (keeping only the last textPaneMaxLines lines),
// optionally pinning the view to the end. Unchanged text is not re-laid out.
func (p *textPane) setText(text string, follow bool) {
	text = tailLines(text, textPaneMaxLines)
	if text != p.text {
		p.text = text
		p.label.SetText(text)
	}
	if follow {
		p.scroll.ScrollToBottom()
	}
}

// tailLines keeps the last n lines of text.
func tailLines(text string, n int) string {
	count := 0
	for i := len(text) - 1; i >= 0; i-- {
		if text[i] == '\n' {
			count++
			if count == n {
				return text[i+1:]
			}
		}
	}
	return text
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

// growingEntry is a multi-line entry that shows all of its lines (within
// [minEntryRows, maxEntryRows]) so the page scroll, not an inner scroller,
// is what moves when the text is long (see newPageScroll).
type growingEntry struct {
	widget.Entry
	rows int
}

const (
	minEntryRows = 3
	maxEntryRows = 12
)

func newGrowingEntry(placeholder string) *growingEntry {
	g := &growingEntry{}
	g.ExtendBaseWidget(g)
	g.MultiLine = true
	g.Wrapping = fyne.TextWrapWord
	g.SetPlaceHolder(placeholder)
	g.fit("")
	return g
}

// setText replaces the text and resizes the entry to show it.
func (g *growingEntry) setText(text string) {
	g.Entry.SetText(text)
	g.fit(text)
}

// fit sizes the entry to text's line count; call it from OnChanged too.
func (g *growingEntry) fit(text string) {
	rows := strings.Count(text, "\n") + 1
	rows = max(minEntryRows, min(maxEntryRows, rows))
	if rows != g.rows {
		g.rows = rows
		g.Entry.SetMinRowsVisible(rows)
	}
}

// wheelRelay is an invisible layer stacked over a page's content. Fyne hands
// a mouse-wheel event to the innermost fyne.Scrollable under the pointer and
// never bubbles it, and every Entry owns an internal scroller — so on a form
// inside a scroll container the page stops scrolling the moment the pointer
// is over a text field. The relay sits above everything on the page, is the
// innermost Scrollable there, and forwards the wheel to the page scroll. It
// implements nothing else, so taps, focus, drags and hover still reach the
// widgets underneath.
type wheelRelay struct {
	widget.BaseWidget
	target *container.Scroll
}

func (w *wheelRelay) CreateRenderer() fyne.WidgetRenderer { return emptyRenderer{} }

func (w *wheelRelay) Scrolled(ev *fyne.ScrollEvent) {
	if w.target != nil {
		w.target.Scrolled(ev)
	}
}

type emptyRenderer struct{}

func (emptyRenderer) Destroy()                     {}
func (emptyRenderer) Layout(fyne.Size)             {}
func (emptyRenderer) MinSize() fyne.Size           { return fyne.Size{} }
func (emptyRenderer) Objects() []fyne.CanvasObject { return nil }
func (emptyRenderer) Refresh()                     {}

// newPageScroll wraps form-like content in a vertical scroll whose wheel keeps
// working over entries (see wheelRelay). Use it only for content with no
// scrollable children of its own — text panes and lists inside it would lose
// the wheel to the page.
func newPageScroll(content fyne.CanvasObject) *container.Scroll {
	relay := &wheelRelay{}
	relay.ExtendBaseWidget(relay)
	scroll := container.NewVScroll(container.NewStack(content, relay))
	relay.target = scroll
	return scroll
}

// sizeObserver is a single-child layout that reports the size it is given.
// Fyne has no resize event, so panels that must react to the viewport
// (e.g. re-fitting a diagram after the window is maximised) wrap their
// content in one of these.
type sizeObserver struct {
	onSize func(fyne.Size)
	last   fyne.Size
}

func newSizeObserver(child fyne.CanvasObject, onSize func(fyne.Size)) *fyne.Container {
	return container.New(&sizeObserver{onSize: onSize}, child)
}

func (l *sizeObserver) MinSize(objects []fyne.CanvasObject) fyne.Size {
	if len(objects) == 0 {
		return fyne.Size{}
	}
	return objects[0].MinSize()
}

func (l *sizeObserver) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	for _, object := range objects {
		object.Resize(size)
		object.Move(fyne.Position{})
	}
	if l.onSize != nil && size != l.last && size.Width > 0 && size.Height > 0 {
		l.last = size
		l.onSize(size)
	}
}
