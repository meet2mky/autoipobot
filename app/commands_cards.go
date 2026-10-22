package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/internal/bridge"
	"autoipo/internal/paths"
)

// commonKeys are offered by the press-key control.
var commonKeys = []string{
	"Enter", "Tab", "Escape", "ArrowDown", "ArrowUp", "ArrowLeft", "ArrowRight",
	"Backspace", "Delete", "Home", "End", "PageDown", "PageUp", "Space",
}

// Navigate card ------------------------------------------------------------

func (p *commandsPanel) navigateCard() fyne.CanvasObject {
	urlEntry := hintEntry("example.com or https://example.com/")
	setURL := widget.NewButton("Set URL", nil)
	setURL.OnTapped = func() {
		validated, err := bridge.ValidateURL(urlEntry.Text)
		if err != nil {
			p.fail(setURL, "set-url", err)
			return
		}
		p.exec(setURL, "set-url", []string{validated}, nil)
	}
	p.gate(setURL, nil, urlEntry)
	open := widget.NewButton("Open saved URL", nil)
	open.OnTapped = func() { p.exec(open, "open", nil, nil) }
	return widget.NewCard("Navigate", "Save the target address, then open it in the extension's tab", container.NewVBox(
		container.NewBorder(nil, nil, nil, setURL, urlEntry),
		container.NewHBox(open),
	))
}

// Interact card ------------------------------------------------------------

func (p *commandsPanel) interactCard() fyne.CanvasObject {
	p.clickSelector = hintEntry(`button[type="submit"]`)
	click := widget.NewButton("Click", nil)
	click.OnTapped = func() { p.exec(click, "click", []string{p.clickSelector.Text}, nil) }
	p.gate(click, nil, p.clickSelector)

	p.fillSelector = hintEntry(`input[name="user_id"]`)
	fillValue := hintEntry("Text to type (never shown in the output)")
	hide := widget.NewCheck("Hide value", func(on bool) {
		fillValue.Password = on
		fillValue.Refresh()
	})
	fill := widget.NewButton("Fill", nil)
	fill.OnTapped = func() { p.exec(fill, "fill", []string{p.fillSelector.Text, fillValue.Text}, nil) }
	p.gate(fill, nil, p.fillSelector, fillValue)

	keySelect := widget.NewSelect(commonKeys, nil)
	keySelect.SetSelected("Enter")
	press := widget.NewButton("Press key", nil)
	press.OnTapped = func() { p.exec(press, "press-key", []string{keySelect.Selected}, nil) }

	dropdown := p.newDropdownControls()
	return widget.NewCard("Interact", "", container.NewVBox(
		fieldRow("Click element", container.NewBorder(nil, nil, nil, click, p.clickSelector)),
		fieldRow("Fill field", container.NewVBox(
			p.fillSelector,
			container.NewBorder(nil, nil, nil, container.NewHBox(hide, fill), fillValue),
		)),
		fieldRow("Press key", container.NewHBox(keySelect, press)),
		fieldRow("Dropdown", dropdown.root()),
	))
}

// dropdownControls loads the choices of a select/autocomplete field and
// picks one of them.
type dropdownControls struct {
	panel     *commandsPanel
	selector  *widget.Entry
	choices   *widget.Select
	load      *widget.Button
	choose    *widget.Button
	loadedFor string
	values    map[string]string // display label -> value sent to choose
}

func (p *commandsPanel) newDropdownControls() *dropdownControls {
	d := &dropdownControls{panel: p, values: map[string]string{}}
	d.selector = hintEntry(`select[name="bank"] or input[name="bank"]`)
	d.choices = widget.NewSelect(nil, nil)
	d.choices.PlaceHolder = "Load choices first"
	d.choices.Disable()
	d.load = widget.NewButton("Load choices", d.loadChoices)
	d.choose = widget.NewButton("Choose", d.chooseSelected)
	d.choose.Disable()
	d.choices.OnChanged = func(string) { d.updateChoose() }
	p.gate(d.load, d.onSelectorChanged, d.selector)
	p.ready[d.choose] = func() bool { return d.choices.Selected != "" && d.loadedFor == strings.TrimSpace(d.selector.Text) }
	return d
}

func (d *dropdownControls) root() fyne.CanvasObject {
	return container.NewVBox(
		container.NewBorder(nil, nil, nil, d.load, d.selector),
		container.NewBorder(nil, nil, nil, d.choose, d.choices),
	)
}

func (d *dropdownControls) onSelectorChanged() {
	if strings.TrimSpace(d.selector.Text) != d.loadedFor {
		d.reset()
	}
}

func (d *dropdownControls) reset() {
	d.loadedFor = ""
	d.values = map[string]string{}
	d.choices.ClearSelected()
	d.choices.SetOptions(nil)
	d.choices.Disable()
	d.choose.Disable()
}

func (d *dropdownControls) updateChoose() {
	setEnabled(d.choose, d.panel.isReady(d.choose))
}

func (d *dropdownControls) loadChoices() {
	selector := strings.TrimSpace(d.selector.Text)
	d.reset()
	d.panel.exec(d.load, "dropdown", []string{selector}, func(_ bridge.Request, result bridge.Result) {
		if len(result.Choices) == 0 {
			d.panel.output.Append("dropdown returned no choices")
			return
		}
		labels := make([]string, 0, len(result.Choices))
		for _, choice := range result.Choices {
			label := choice.String()
			labels = append(labels, label)
			value := choice.Value
			if value == "" {
				value = choice.Label
			}
			d.values[label] = value
		}
		d.loadedFor = selector
		d.choices.SetOptions(labels)
		d.choices.Enable()
		d.choices.SetSelected(labels[0])
		d.updateChoose()
	})
}

func (d *dropdownControls) chooseSelected() {
	value, ok := d.values[d.choices.Selected]
	if !ok || d.loadedFor == "" {
		d.panel.fail(d.choose, "choose", errors.New("load choices for the current selector first"))
		return
	}
	d.panel.exec(d.choose, "choose", []string{d.loadedFor, value}, nil)
}

// Inspect card -------------------------------------------------------------

func (p *commandsPanel) inspectCard() fyne.CanvasObject {
	list := widget.NewButton("List selectors", nil)
	p.selectorRows = container.NewVBox(widget.NewLabel("Click List selectors to scan the current page."))
	list.OnTapped = func() {
		p.exec(list, "list-selectors", nil, func(_ bridge.Request, result bridge.Result) {
			p.renderSelectors(result.Elements)
		})
	}

	containsSelector := hintEntry(".account-dashboard")
	contains := widget.NewButton("Page contains?", nil)
	contains.OnTapped = func() { p.exec(contains, "page-contains", []string{containsSelector.Text}, nil) }
	p.gate(contains, nil, containsSelector)

	readSelector := hintEntry("#balance")
	read := widget.NewButton("Read text", nil)
	read.OnTapped = func() { p.exec(read, "read-text", []string{readSelector.Text}, nil) }
	p.gate(read, nil, readSelector)

	shot := widget.NewButton("Screenshot", nil)
	shot.OnTapped = func() { p.takeScreenshot(shot) }
	p.screenshotLabel = wrappedLabel("")

	return widget.NewCard("Inspect", "", container.NewVBox(
		fieldRow("Elements on the page", container.NewVBox(container.NewHBox(list), p.selectorRows)),
		fieldRow("Page contains", container.NewBorder(nil, nil, nil, contains, containsSelector)),
		fieldRow("Read text", container.NewBorder(nil, nil, nil, read, readSelector)),
		fieldRow("Screenshot", container.NewVBox(container.NewHBox(shot), p.screenshotLabel)),
	))
}

// maxSelectorRows bounds the inline element list; the card scrolls with the
// rest of the tab instead of nesting a second scroll area, and the full list
// is always in the Output pane.
const maxSelectorRows = 40

// renderSelectors shows the elements found by list-selectors with a "Use"
// button that copies the selector into the Click and Fill fields.
func (p *commandsPanel) renderSelectors(elements []bridge.SelectorMetadata) {
	p.selectorRows.Objects = nil
	if len(elements) == 0 {
		p.selectorRows.Add(widget.NewLabel("No visible clickable or fillable elements found."))
	}
	if len(elements) > maxSelectorRows {
		p.selectorRows.Add(wrappedLabel(fmt.Sprintf("Showing the first %d of %d elements; the complete list is in the Output pane.", maxSelectorRows, len(elements))))
		elements = elements[:maxSelectorRows]
	}
	for _, element := range elements {
		description := element.Kind + " · " + firstNonBlank(element.Label, element.Description)
		if element.Context != "" {
			description += " (for: " + element.Context + ")"
		}
		selectorLabel := widget.NewLabel(element.Selector)
		selectorLabel.TextStyle = fyne.TextStyle{Monospace: true}
		selectorLabel.Truncation = fyne.TextTruncateEllipsis
		selector := element.Selector
		use := widget.NewButton("Use", func() {
			p.clickSelector.SetText(selector)
			p.fillSelector.SetText(selector)
			p.output.Append("selector copied into Click and Fill: " + selector)
		})
		p.selectorRows.Add(container.NewBorder(nil, nil, nil, use, container.NewVBox(widget.NewLabel(description), selectorLabel)))
	}
	p.selectorRows.Refresh()
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (p *commandsPanel) takeScreenshot(button *widget.Button) {
	request := bridge.Request{Type: bridge.TypeScreenshot}
	var saved string
	p.invoke(button, bridge.FormatCommand("screenshot", nil), commandTimeout, func(ctx context.Context) (string, error) {
		result, err := p.ctx.bridge.Send(ctx, request)
		if err != nil {
			return "", err
		}
		png, err := bridge.DecodeScreenshot(result)
		if err != nil {
			return "", fmt.Errorf("decode screenshot: %w", err)
		}
		if len(png) == 0 {
			return "", errors.New("the extension returned an empty screenshot")
		}
		path, err := saveScreenshot(png)
		if err != nil {
			return "", err
		}
		saved = path
		return "screenshot saved to " + path, nil
	}, func() { p.screenshotLabel.SetText("Last screenshot: " + saved) })
}

// saveScreenshot writes the image under .data/screenshots (extension chosen
// from the bytes) and returns its path.
func saveScreenshot(image []byte) (string, error) {
	dir := paths.Data("screenshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create screenshot directory: %w", err)
	}
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+bridge.ImageExtension(image))
	if err := os.WriteFile(path, image, 0o600); err != nil {
		return "", fmt.Errorf("write screenshot: %w", err)
	}
	return path, nil
}
