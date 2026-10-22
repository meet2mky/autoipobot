package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/outputlog"
	"autoipo/internal/bridge"
	"autoipo/internal/paths"
)

// Timeouts for manual bridge interactions.
const (
	commandTimeout     = 60 * time.Second
	bridgeStartTimeout = 20 * time.Second
	bridgeReadyTimeout = 10 * time.Second
	bridgeStopTimeout  = 3 * time.Second
)

// commandWork performs one bridge interaction off the UI thread and returns
// the text to show in the output pane.
type commandWork func(ctx context.Context) (string, error)

// commandsPanel is the Commands tab: manual bridge commands grouped into
// cards, with a timestamped output pane.
type commandsPanel struct {
	ctx *appContext

	output       *outputlog.Log
	outputPane   *textPane
	outputFilter *widget.Entry
	progress     *widget.ProgressBarInfinite
	busy         map[*widget.Button]bool // buttons with a command in flight
	ready        map[*widget.Button]func() bool

	bridgeLine     *widget.Label
	startBridgeBtn *widget.Button
	stopBridgeBtn  *widget.Button

	clickSelector   *widget.Entry
	fillSelector    *widget.Entry
	selectorRows    *fyne.Container
	screenshotLabel *widget.Label

	root fyne.CanvasObject
}

func newCommandsPanel(ctx *appContext) *commandsPanel {
	p := &commandsPanel{
		ctx:    ctx,
		output: outputlog.New(outputlog.DefaultCapacity),
		busy:   map[*widget.Button]bool{},
		ready:  map[*widget.Button]func() bool{},
	}
	outputCard := p.buildOutputCard()
	cards := container.NewVBox(p.bridgeCard(), p.navigateCard(), p.interactCard(), p.inspectCard())
	split := container.NewHSplit(container.NewVScroll(cards), outputCard)
	split.SetOffset(0.58)
	p.root = split
	p.output.SetOnChange(func() { fyne.Do(p.renderOutput) })
	ctx.status.subscribe(p.applyStatus)
	return p
}

// Output pane --------------------------------------------------------------

func (p *commandsPanel) buildOutputCard() fyne.CanvasObject {
	p.outputPane = newTextPane("Commands sent and responses received appear here.")
	p.outputFilter = hintEntry("Filter output…")
	p.outputFilter.OnChanged = func(string) { p.renderOutput() }
	clear := widget.NewButton("Clear", p.output.Clear)
	p.progress = widget.NewProgressBarInfinite()
	p.progress.Hide()
	bar := container.NewBorder(nil, nil, nil, clear, p.outputFilter)
	return widget.NewCard("Output", "", container.NewBorder(container.NewVBox(bar, p.progress), nil, nil, nil, p.outputPane.scroll))
}

func (p *commandsPanel) renderOutput() {
	text := p.output.Filter(p.outputFilter.Text)
	if text == "" {
		if p.output.Len() == 0 {
			text = "Commands sent and responses received appear here."
		} else {
			text = "No matching output."
		}
	}
	p.outputPane.setText(text, true)
}

// Execution helpers --------------------------------------------------------

// invoke runs work in a goroutine, shows the progress bar while it runs,
// disables only the tapped button and tints it with the outcome. after runs
// on the UI thread when the command succeeded.
func (p *commandsPanel) invoke(button *widget.Button, label string, timeout time.Duration, work commandWork, after func()) {
	p.output.Append("-> " + label)
	p.begin(button)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		text, err := work(ctx)
		fyne.Do(func() {
			p.end(button, err == nil)
			if err != nil {
				p.output.Append("<- FAIL " + err.Error())
				return
			}
			p.output.Append("<- OK " + strings.TrimSpace(text))
			if after != nil {
				after()
			}
		})
	}()
}

func (p *commandsPanel) begin(button *widget.Button) {
	button.Disable()
	resetButton(button)
	p.busy[button] = true
	p.progress.Show()
	p.progress.Start()
}

func (p *commandsPanel) end(button *widget.Button, ok bool) {
	delete(p.busy, button)
	if len(p.busy) == 0 {
		p.progress.Stop()
		p.progress.Hide()
	}
	tintButton(button, ok)
	setEnabled(button, p.isReady(button))
}

// isReady reports whether button may be enabled: not in flight and its
// input gate (if any) satisfied.
func (p *commandsPanel) isReady(button *widget.Button) bool {
	if p.busy[button] {
		return false
	}
	if ready, ok := p.ready[button]; ok {
		return ready()
	}
	return true
}

// gate enables button only while every entry has text and re-applies that
// rule when a command finishes. extra runs on every change (may be nil).
func (p *commandsPanel) gate(button *widget.Button, extra func(), entries ...*widget.Entry) {
	ready := func() bool {
		for _, entry := range entries {
			if strings.TrimSpace(entry.Text) == "" {
				return false
			}
		}
		return true
	}
	p.ready[button] = ready
	for _, entry := range entries {
		entry.OnChanged = func(string) {
			setEnabled(button, p.isReady(button))
			if extra != nil {
				extra()
			}
		}
	}
	setEnabled(button, p.isReady(button))
}

// exec sends a CLI-style command through the bridge client. after receives
// the request and result on the UI thread when the command succeeds.
func (p *commandsPanel) exec(button *widget.Button, name string, args []string, after func(bridge.Request, bridge.Result)) {
	request, err := bridge.BuildRequest(name, args)
	if err != nil {
		p.fail(button, name, err)
		return
	}
	var result bridge.Result
	p.invoke(button, bridge.FormatCommand(name, args), commandTimeout, func(ctx context.Context) (string, error) {
		var sendErr error
		result, sendErr = p.ctx.bridge.Send(ctx, request)
		if sendErr != nil {
			return "", sendErr
		}
		return bridge.FormatResult(request, result), nil
	}, func() {
		if after != nil {
			after(request, result)
		}
	})
}

// fail reports a local validation problem without touching the bridge.
func (p *commandsPanel) fail(button *widget.Button, name string, err error) {
	p.output.Appendf("[%s] %v", name, err)
	tintButton(button, false)
}

// Bridge card --------------------------------------------------------------

func (p *commandsPanel) bridgeCard() fyne.CanvasObject {
	p.bridgeLine = wrappedLabel("Checking bridge…")
	p.startBridgeBtn = widget.NewButton("Start bridge", p.startBridge)
	p.startBridgeBtn.Importance = widget.HighImportance
	p.stopBridgeBtn = widget.NewButton("Stop bridge", p.stopBridge)
	p.stopBridgeBtn.Disable()
	help := wrappedLabel("Keep Chrome open with the autoipo extension enabled; commands go to the extension's target tab.")
	return widget.NewCard("Bridge", "", container.NewVBox(p.bridgeLine, container.NewHBox(p.startBridgeBtn, p.stopBridgeBtn), help))
}

func (p *commandsPanel) applyStatus(s statusSnapshot) {
	var line string
	switch {
	case !s.BridgeReachable:
		line = "Bridge is not running — click Start bridge."
	case s.Bridge.ExtensionConnected:
		line = fmt.Sprintf("Bridge reachable · extension connected (protocol v%d)", s.Bridge.ProtocolVersion)
	default:
		line = "Bridge reachable · waiting for the Chrome extension to connect"
	}
	if s.BridgeOwned {
		line += " · started by this app"
	}
	p.bridgeLine.SetText(line)
	setEnabled(p.startBridgeBtn, !s.BridgeReachable && p.isReady(p.startBridgeBtn))
	setEnabled(p.stopBridgeBtn, s.BridgeOwned && p.isReady(p.stopBridgeBtn))
}

func (p *commandsPanel) startBridge() {
	p.invoke(p.startBridgeBtn, "start bridge (chrome-extension-cli serve)", bridgeStartTimeout, func(ctx context.Context) (string, error) {
		defer p.ctx.status.refreshSoon()
		output := newEventWriter(p.ctx.events, "[bridge] ")
		process, err := bridge.EnsureServing(ctx, p.ctx.bridge, paths.CLIBinary(), output, bridgeReadyTimeout)
		if err != nil {
			return "", err
		}
		if process == nil {
			return "a bridge is already running", nil
		}
		p.ctx.adoptBridgeProcess(process)
		return fmt.Sprintf("bridge started (pid %d); waiting for the Chrome extension", process.PID()), nil
	}, nil)
}

func (p *commandsPanel) stopBridge() {
	process := p.ctx.ownedBridge()
	if process == nil {
		if p.ctx.status.latest().BridgeReachable {
			p.output.Append("The running bridge was not started by this app (the daemon or a terminal owns it); stop it from there.")
		} else {
			p.output.Append("No bridge is running.")
		}
		return
	}
	p.invoke(p.stopBridgeBtn, "stop bridge", bridgeStopTimeout+2*time.Second, func(context.Context) (string, error) {
		defer p.ctx.status.refreshSoon()
		if err := process.Stop(bridgeStopTimeout); err != nil {
			return "", err
		}
		return "bridge stopped", nil
	}, nil)
}
