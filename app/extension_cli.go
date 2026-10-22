package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

type extensionCLIController struct {
	root      string
	binary    string
	moduleDir string
	mu        sync.Mutex
	server    *exec.Cmd
	built     bool
}

type commandInput struct {
	*widget.Entry
	width  float32
	row    *fyne.Container
	change func(string)
}

type commandInputRowLayout struct {
	inputs map[fyne.CanvasObject]*commandInput
}

func (layout *commandInputRowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	var width, height float32
	for _, object := range objects {
		objectSize := object.MinSize()
		if input, ok := layout.inputs[object]; ok {
			objectSize.Width = input.width
		}
		width += objectSize.Width
		if objectSize.Height > height {
			height = objectSize.Height
		}
	}
	if len(objects) > 1 {
		width += float32(len(objects)-1) * 8
	}
	return fyne.NewSize(width, height)
}

func (layout *commandInputRowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	x := float32(0)
	for _, object := range objects {
		objectSize := object.MinSize()
		if input, ok := layout.inputs[object]; ok {
			objectSize.Width = input.width
		}
		object.Move(fyne.NewPos(x, (size.Height-objectSize.Height)/2))
		object.Resize(objectSize)
		x += objectSize.Width + 8
	}
}

func newCommandInput(placeholder string) *commandInput {
	input := &commandInput{Entry: widget.NewEntry()}
	input.SetPlaceHolder(placeholder)
	input.updateWidth("")
	input.Entry.OnChanged = func(value string) {
		input.updateWidth(value)
		if input.change != nil {
			input.change(value)
		}
	}
	return input
}

func (input *commandInput) setOnChanged(change func(string)) {
	input.change = change
}

func (input *commandInput) setRow(row *fyne.Container) {
	input.row = row
	input.resizeRow()
}

func newCommandInputRow(inputs []*commandInput, controls ...fyne.CanvasObject) *fyne.Container {
	objects := make([]fyne.CanvasObject, 0, len(inputs)+len(controls))
	inputObjects := make(map[fyne.CanvasObject]*commandInput, len(inputs))
	for _, input := range inputs {
		objects = append(objects, input.Entry)
		inputObjects[input.Entry] = input
	}
	objects = append(objects, controls...)
	row := container.New(&commandInputRowLayout{inputs: inputObjects}, objects...)
	for _, input := range inputs {
		input.setRow(row)
	}
	return row
}

func (input *commandInput) updateWidth(value string) {
	baseline := fyne.MeasureText(strings.Repeat("M", 20), theme.TextSize(), fyne.TextStyle{}).Width
	textWidth := fyne.MeasureText(value, theme.TextSize(), fyne.TextStyle{}).Width
	input.width = baseline + 32
	if textWidth+32 > input.width {
		input.width = textWidth + 32
	}
	input.resizeRow()
}

func (input *commandInput) resizeRow() {
	if input.row != nil && input.row.Size().Width > 0 {
		input.row.Resize(input.row.Size())
		input.row.Refresh()
	}
}

func newExtensionCLIController(root string) *extensionCLIController {
	binaryName := "chrome-extension-cli"
	return &extensionCLIController{
		root:      root,
		moduleDir: filepath.Join(root, "chrome-extension-cli"),
		binary:    filepath.Join(root, "bin", binaryName),
	}
}

func (c *extensionCLIController) Build() error {
	c.mu.Lock()
	c.built = false
	c.mu.Unlock()
	if err := installBundledHelpers(c.root); err != nil {
		return err
	}
	c.mu.Lock()
	c.built = true
	c.mu.Unlock()
	return nil
}

func (c *extensionCLIController) Run(args ...string) (string, error) {
	defer waitAfterCLIAction()
	c.mu.Lock()
	built := c.built
	c.mu.Unlock()
	if !built {
		return "", fmt.Errorf("the latest chrome-extension-cli build is not ready yet")
	}
	if _, err := os.Stat(c.binary); err != nil {
		return "", fmt.Errorf("chrome-extension-cli is not built yet")
	}
	if len(args) > 0 && commandNeedsExtension(args[0]) {
		if err := waitForExtensionConnection(c); err != nil {
			return "", err
		}
	}
	cmd := exec.Command(c.binary, args...)
	cmd.Dir = c.moduleDir
	output, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func waitAfterCLIAction() {
	time.Sleep(time.Duration(2000+rand.Intn(2001)) * time.Millisecond)
}

func commandNeedsExtension(command string) bool {
	return command != "serve"
}

func waitForExtensionConnection(controller *extensionCLIController) error {
	if !controller.ServeRunning() && !bridgeIsReachable() {
		return fmt.Errorf("start serve first; the extension bridge is not running")
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		connected, supported := extensionBridgeConnected()
		if connected {
			return nil
		}
		if !supported && bridgeIsReachable() {
			return fmt.Errorf("the running bridge cannot report whether Chrome is connected; stop the older serve process and start serve from this app")
		}
		time.Sleep(150 * time.Millisecond)
	}
	if bridgeIsReachable() {
		return fmt.Errorf("Chrome extension is not connected to the bridge; check that Chrome is open and the extension is enabled")
	}
	return fmt.Errorf("extension bridge is not responding; start serve first")
}

func extensionBridgeConnected() (connected, supported bool) {
	client := http.Client{Timeout: 300 * time.Millisecond}
	response, err := client.Get("http://127.0.0.1:18765/status")
	if err != nil {
		return false, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, false
	}
	var status struct {
		Serving            bool `json:"serving"`
		ExtensionConnected bool `json:"extension_connected"`
	}
	if json.NewDecoder(response.Body).Decode(&status) != nil || !status.Serving {
		return false, false
	}
	return status.ExtensionConnected, true
}

func (c *extensionCLIController) StartServe(output io.Writer) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.server != nil {
		return fmt.Errorf("serve is already running")
	}
	if !c.built {
		return fmt.Errorf("the latest chrome-extension-cli build is not ready yet")
	}
	if bridgeIsReachable() {
		return fmt.Errorf("the extension bridge is already serving on 127.0.0.1:18765")
	}
	if _, err := os.Stat(c.binary); err != nil {
		return fmt.Errorf("chrome-extension-cli is not built yet")
	}
	cmd := exec.Command(c.binary, "serve")
	cmd.Dir = c.moduleDir
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start extension bridge: %w", err)
	}
	c.server = cmd
	go func() {
		err := cmd.Wait()
		c.mu.Lock()
		if c.server == cmd {
			c.server = nil
		}
		c.mu.Unlock()
		if err != nil {
			_, _ = fmt.Fprintf(output, "serve exited: %v\n", err)
		} else {
			_, _ = io.WriteString(output, "serve stopped\n")
		}
	}()
	return nil
}

func (c *extensionCLIController) StopServe() error {
	c.mu.Lock()
	server := c.server
	c.mu.Unlock()
	if server == nil || server.Process == nil {
		return fmt.Errorf("serve is not running")
	}
	if err := server.Process.Kill(); err != nil {
		return fmt.Errorf("stop extension bridge: %w", err)
	}
	return nil
}

func (c *extensionCLIController) ServeRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.server != nil
}

func (c *extensionCLIController) Close() {
	_ = c.StopServe()
}

func bridgeIsReachable() bool {
	client := http.Client{Timeout: 500 * time.Millisecond}
	response, err := client.Get("http://127.0.0.1:18765/health")
	if err != nil {
		return false
	}
	_ = response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func (state *appState) appendCLIOutput(message string) {
	state.cliOutputMu.Lock()
	state.cliOutputLines = append(state.cliOutputLines, message)
	text := state.filteredCLIOutputLocked()
	state.cliOutputMu.Unlock()
	fyne.Do(func() {
		if state.cliOutput != nil {
			state.cliOutput.SetText(text)
		}
	})
}

func (state *appState) filteredCLIOutputLocked() string {
	query := strings.TrimSpace(state.cliOutputSearch)
	if query == "" {
		return strings.Join(state.cliOutputLines, "\n")
	}
	var matches []string
	loweredQuery := strings.ToLower(query)
	for _, output := range state.cliOutputLines {
		for _, line := range strings.Split(output, "\n") {
			if strings.Contains(strings.ToLower(line), loweredQuery) {
				matches = append(matches, line)
			}
		}
	}
	if len(matches) == 0 {
		return "No matching output."
	}
	return strings.Join(matches, "\n")
}

func (state *appState) clearCLIOutput() {
	state.cliOutputMu.Lock()
	state.cliOutputLines = nil
	text := state.filteredCLIOutputLocked()
	state.cliOutputMu.Unlock()
	fyne.Do(func() {
		if state.cliOutput != nil {
			state.cliOutput.SetText(text)
		}
	})
}

func newCommandsPanel(state *appState) fyne.CanvasObject {
	buildStatus := widget.NewLabel("Installing bundled chrome-extension-cli…")
	state.cliBuildStatus = buildStatus
	requirement := widget.NewLabel("Requirement: start serve and keep Chrome open with the extension connected before running any command.")
	requirement.Wrapping = fyne.TextWrapWord
	serveStatus := widget.NewLabel("Bridge is stopped")
	startServeButton := widget.NewButton("Start serve", nil)
	stopServeButton := widget.NewButton("Stop serve", nil)
	stopServeButton.Disable()
	startServeButton.OnTapped = func() {
		startServeButton.Disable()
		setCommandButtonResult(startServeButton, 0)
		state.clearCLIOutput()
		state.appendCLIOutput("[Request from AutoIPO app] -> chrome-extension-cli \"serve\"")
		if err := state.extensionCLI.StartServe(io.Discard); err != nil {
			state.appendCLIOutput("[Response from chrome-extension-cli] <- FAIL " + err.Error())
			setCommandButtonResult(startServeButton, -1)
			startServeButton.Enable()
			if bridgeIsReachable() {
				serveStatus.SetText("Bridge is already serving")
			}
			return
		}
		serveStatus.SetText("Bridge process started; waiting for readiness…")
		stopServeButton.Enable()
		go func() {
			waitAfterCLIAction()
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if !state.extensionCLI.ServeRunning() {
					fyne.Do(func() {
						serveStatus.SetText("Bridge stopped; see command output for details")
						setCommandButtonResult(startServeButton, -1)
						stopServeButton.Disable()
						startServeButton.Enable()
					})
					return
				}
				connected, supported := extensionBridgeConnected()
				if connected {
					fyne.Do(func() {
						serveStatus.SetText("Bridge and Chrome extension are connected")
						setCommandButtonResult(startServeButton, 1)
					})
					return
				}
				if bridgeIsReachable() {
					fyne.Do(func() {
						if supported {
							serveStatus.SetText("Bridge ready; waiting for Chrome extension…")
						} else {
							serveStatus.SetText("Bridge is ready on 127.0.0.1:18765")
						}
						setCommandButtonResult(startServeButton, 1)
					})
				}
				time.Sleep(100 * time.Millisecond)
			}
			fyne.Do(func() {
				if bridgeIsReachable() {
					serveStatus.SetText("Bridge ready; Chrome extension is not connected")
					setCommandButtonResult(startServeButton, 1)
				} else {
					serveStatus.SetText("Serve started; bridge readiness could not be confirmed")
					setCommandButtonResult(startServeButton, -1)
				}
			})
		}()
	}
	stopServeButton.OnTapped = func() {
		stopServeButton.Disable()
		state.clearCLIOutput()
		state.appendCLIOutput("[Request from AutoIPO app] -> stop serve")
		go func() {
			err := state.extensionCLI.StopServe()
			waitAfterCLIAction()
			fyne.Do(func() {
				if err != nil {
					state.appendCLIOutput("[Response from chrome-extension-cli] <- FAIL " + err.Error())
					setCommandButtonResult(stopServeButton, -1)
					stopServeButton.Enable()
					return
				}
				state.appendCLIOutput("[Response from chrome-extension-cli] <- OK bridge stopped")
				setCommandButtonResult(stopServeButton, 1)
				serveStatus.SetText("Bridge stopped")
				startServeButton.Enable()
			})
		}()
	}

	setURLInput := newCommandInput("example.com or https://example.com/")
	setURLButton := widget.NewButton("Set URL", nil)
	setURLButton.Disable()
	setURLInput.setOnChanged(func(value string) {
		setButtonEnabled(setURLButton, strings.TrimSpace(value) != "")
	})
	setURLRow := newCommandInputRow([]*commandInput{setURLInput}, setURLButton)
	setURLButton.OnTapped = func() {
		state.clearCLIOutput()
		websiteURL, err := normalizeExtensionWebsiteURL(setURLInput.Text)
		if err != nil {
			state.appendCLIOutput("[failure] set-url: " + err.Error())
			setCommandButtonResult(setURLButton, -1)
			return
		}
		runCLIButtonWhenReady(state, setURLButton, "set-url", "Saved URL: "+websiteURL, func() bool { return strings.TrimSpace(setURLInput.Text) != "" }, "set-url", websiteURL)
	}

	openButton := widget.NewButton("Open saved URL", nil)
	openButton.OnTapped = func() { runCLIButton(state, openButton, "open", "open") }

	clickSelector := newCommandInput(`button[type="submit"]`)
	clickButton := widget.NewButton("Click element", nil)
	clickButton.Disable()
	clickSelector.setOnChanged(func(value string) {
		setButtonEnabled(clickButton, strings.TrimSpace(value) != "")
	})
	clickRow := newCommandInputRow([]*commandInput{clickSelector}, clickButton)
	clickButton.OnTapped = func() {
		runCLIButtonWhenReady(state, clickButton, "click", "", func() bool { return strings.TrimSpace(clickSelector.Text) != "" }, "click", clickSelector.Text)
	}

	fillSelector := newCommandInput(`input[name="user_id"]`)
	fillValue := newCommandInput("Text to enter")
	fillButton := widget.NewButton("Fill field", nil)
	fillButton.Disable()
	updateFillButton := func() {
		setButtonEnabled(fillButton, strings.TrimSpace(fillSelector.Text) != "" && strings.TrimSpace(fillValue.Text) != "")
	}
	fillSelector.setOnChanged(func(string) { updateFillButton() })
	fillValue.setOnChanged(func(string) { updateFillButton() })
	fillRow := newCommandInputRow([]*commandInput{fillSelector, fillValue}, fillButton)
	fillButton.OnTapped = func() {
		runCLIButtonWhenReady(state, fillButton, "fill", "", func() bool { return strings.TrimSpace(fillSelector.Text) != "" && strings.TrimSpace(fillValue.Text) != "" }, "fill", fillSelector.Text, fillValue.Text)
	}

	dropdownSelector := newCommandInput(`select[name="bank"] or input[name="bank"]`)
	loadedDropdownSelector := ""
	dropdownOptions := widget.NewSelect([]string{}, nil)
	dropdownOptions.PlaceHolder = "Load choices first"
	dropdownOptions.Disable()
	loadDropdownButton := widget.NewButton("Load dropdown choices", nil)
	loadDropdownButton.Disable()
	chooseDropdownButton := widget.NewButton("Choose selected option", nil)
	chooseDropdownButton.Disable()
	dropdownSelector.setOnChanged(func(value string) {
		selector := strings.TrimSpace(value)
		setButtonEnabled(loadDropdownButton, selector != "")
		if selector != loadedDropdownSelector {
			loadedDropdownSelector = ""
			dropdownOptions.Options = nil
			dropdownOptions.SetSelected("")
			dropdownOptions.Disable()
			chooseDropdownButton.Disable()
		}
	})
	dropdownRow := newCommandInputRow([]*commandInput{dropdownSelector}, loadDropdownButton)
	dropdownOptions.OnChanged = func(selected string) {
		setButtonEnabled(chooseDropdownButton, selected != "" && loadedDropdownSelector != "" && strings.TrimSpace(dropdownSelector.Text) == loadedDropdownSelector)
	}
	loadDropdownButton.OnTapped = func() {
		state.clearCLIOutput()
		selector := strings.TrimSpace(dropdownSelector.Text)
		if selector == "" {
			state.appendCLIOutput("Dropdown: enter a CSS selector first.")
			return
		}
		loadDropdownButton.Disable()
		setCommandButtonResult(loadDropdownButton, 0)
		state.appendCLIOutput("[Request from AutoIPO app] -> " + formatCLICommand("dropdown", selector))
		loadedDropdownSelector = ""
		dropdownOptions.Options = nil
		dropdownOptions.SetSelected("")
		dropdownOptions.Disable()
		chooseDropdownButton.Disable()
		go func() {
			output, err := state.extensionCLI.Run("dropdown", selector)
			options := parseCLIOptions(output)
			fyne.Do(func() {
				defer func() { setButtonEnabled(loadDropdownButton, strings.TrimSpace(dropdownSelector.Text) != "") }()
				appendCLIResult(state, []string{"dropdown", selector}, output, err)
				if strings.TrimSpace(dropdownSelector.Text) != selector {
					state.appendCLIOutput("[Response from AutoIPO app] <- FAIL dropdown selector changed while choices were loading")
					setCommandButtonResult(loadDropdownButton, -1)
					return
				}
				if err != nil {
					setCommandButtonResult(loadDropdownButton, -1)
					return
				}
				if len(options) == 0 {
					state.appendCLIOutput("[Response from AutoIPO app] <- FAIL dropdown returned no choices")
					setCommandButtonResult(loadDropdownButton, -1)
					return
				}
				displays := make([]string, 0, len(options))
				for _, option := range options {
					displays = append(displays, option.Display)
				}
				dropdownOptions.Options = displays
				dropdownOptions.Enable()
				dropdownOptions.SetSelected(displays[0])
				loadedDropdownSelector = selector
				chooseDropdownButton.Enable()
				setCommandButtonResult(loadDropdownButton, 1)
				state.appendCLIOutput(fmt.Sprintf("[Response from AutoIPO app] <- OK loaded %d choices for %s", len(options), selector))
			})
		}()
	}
	chooseDropdownButton.OnTapped = func() {
		state.clearCLIOutput()
		if strings.TrimSpace(dropdownSelector.Text) != loadedDropdownSelector || loadedDropdownSelector == "" {
			state.appendCLIOutput("[failure] choose: load choices for the current selector first.")
			setCommandButtonResult(chooseDropdownButton, -1)
			return
		}
		selected := dropdownOptions.Selected
		if selected == "" {
			state.appendCLIOutput("[failure] choose: load and select a dropdown option first.")
			setCommandButtonResult(chooseDropdownButton, -1)
			return
		}
		value := selectedOptionValue(selected)
		runCLIButtonWhenReady(state, chooseDropdownButton, "choose", "", func() bool {
			return loadedDropdownSelector != "" && strings.TrimSpace(dropdownSelector.Text) == loadedDropdownSelector && dropdownOptions.Selected != ""
		}, "choose", dropdownSelector.Text, value)
	}

	listSelectorsButton := widget.NewButton("List page selectors", nil)
	listSelectorsButton.OnTapped = func() {
		runCLIButton(state, listSelectorsButton, "list-selectors", "list-selectors")
	}
	containsSelector := newCommandInput(`.account-dashboard`)
	containsButton := widget.NewButton("Check selector", nil)
	containsButton.Disable()
	containsSelector.setOnChanged(func(value string) {
		setButtonEnabled(containsButton, strings.TrimSpace(value) != "")
	})
	containsRow := newCommandInputRow([]*commandInput{containsSelector}, containsButton)
	containsButton.OnTapped = func() {
		runCLIButtonWhenReady(state, containsButton, "page-contains", "", func() bool {
			return strings.TrimSpace(containsSelector.Text) != ""
		}, "page-contains", containsSelector.Text)
	}

	sleepSeconds := newCommandInput("Maximum seconds, e.g. 5")
	sleepButton := widget.NewButton("Run random sleep", nil)
	sleepButton.Disable()
	sleepSeconds.setOnChanged(func(value string) {
		setButtonEnabled(sleepButton, strings.TrimSpace(value) != "")
	})
	sleepRow := newCommandInputRow([]*commandInput{sleepSeconds}, sleepButton)
	sleepButton.OnTapped = func() {
		state.clearCLIOutput()
		if strings.TrimSpace(sleepSeconds.Text) == "" {
			state.appendCLIOutput("[failure] random-sleep: enter a maximum duration in seconds.")
			setCommandButtonResult(sleepButton, -1)
			return
		}
		runCLIButtonWhenReady(state, sleepButton, "random-sleep", "", func() bool { return strings.TrimSpace(sleepSeconds.Text) != "" }, "random-sleep", sleepSeconds.Text)
	}

	helpButton := widget.NewButton("Show help", nil)
	helpButton.OnTapped = func() { runCLIButton(state, helpButton, "help", "help") }

	state.cliOutput = widget.NewMultiLineEntry()
	state.cliOutput.Disable()
	state.cliOutput.Wrapping = fyne.TextWrapWord
	state.cliOutput.SetMinRowsVisible(12)
	outputSearch := widget.NewEntry()
	outputSearch.SetPlaceHolder("Search output or selectors…")
	outputSearch.OnChanged = func(value string) {
		state.cliOutputMu.Lock()
		state.cliOutputSearch = value
		text := state.filteredCLIOutputLocked()
		state.cliOutputMu.Unlock()
		state.cliOutput.SetText(text)
	}
	state.cliOutputMu.Lock()
	state.cliOutput.SetText(state.filteredCLIOutputLocked())
	state.cliOutputMu.Unlock()

	serveCard := widget.NewCard("1. serve", "Start the long-running local bridge before sending page commands.", container.NewVBox(serveStatus, container.NewHBox(startServeButton, stopServeButton)))
	setURLCard := widget.NewCard("2. set-url", "Save the URL that the extension will open.", setURLRow)
	openCard := widget.NewCard("3. open", "Open the saved URL in the extension's target tab.", container.NewHBox(openButton))
	clickCard := widget.NewCard("4. click", "Click one element selected by CSS selector.", clickRow)
	fillCard := widget.NewCard("5. fill", "Enter text in one field. The value is not shown in command output.", fillRow)
	dropdownCard := widget.NewCard("6. dropdown", "Load choices from the field below. For autocomplete fields, open the list or type a search value first.", dropdownRow)
	chooseCard := widget.NewCard("7. choose", "Select one of the choices loaded by the dropdown command above.", newCommandInputRow(nil, dropdownOptions, chooseDropdownButton))
	sleepCard := widget.NewCard("8. random-sleep", "Wait for a random duration up to the entered number of seconds.", sleepRow)
	selectorsCard := widget.NewCard("8. list-selectors", "Scan the current target tab and return selectors with kind, label, description, and context metadata.", container.NewHBox(listSelectorsButton))
	containsCard := widget.NewCard("9. page-contains", "Return true when the current page contains a matching CSS selector.", containsRow)
	helpCard := widget.NewCard("11. help", "Show the CLI usage and supported commands.", container.NewHBox(helpButton))
	buildCard := widget.NewCard("Bundled CLI", "The latest CLI is built during deployment and packaged inside the desktop app.", buildStatus)
	content := container.NewVBox(serveCard, requirement, setURLCard, openCard, clickCard, fillCard, dropdownCard, chooseCard, selectorsCard, containsCard, sleepCard, helpCard, buildCard)
	commandsScroll := container.NewVScroll(content)
	outputCard := widget.NewCard("Output", "Commands sent and responses received appear here.", container.NewBorder(outputSearch, nil, nil, nil, state.cliOutput))
	split := container.NewHSplit(commandsScroll, outputCard)
	split.SetOffset(0.68)
	return split
}

func setButtonEnabled(button *widget.Button, enabled bool) {
	if enabled {
		button.Enable()
	} else {
		button.Disable()
	}
}

func setCommandButtonResult(button *widget.Button, result int) {
	switch result {
	case 1:
		button.Importance = widget.SuccessImportance
	case -1:
		button.Importance = widget.DangerImportance
	default:
		button.Importance = widget.MediumImportance
	}
	button.Refresh()
}

func formatCLICommand(args ...string) string {
	parts := []string{"chrome-extension-cli"}
	for index, arg := range args {
		if len(args) > 0 && args[0] == "fill" && index == 2 {
			parts = append(parts, "[value redacted]")
			continue
		}
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func appendCLIResult(state *appState, args []string, output string, err error) {
	output = strings.TrimSpace(output)
	if err != nil {
		state.appendCLIOutput("[Response from chrome-extension-cli] <- FAIL " + err.Error())
		if output != "" {
			state.appendCLIOutput("[Response output from chrome-extension-cli] <- " + output)
		}
	} else {
		message := "[Response from chrome-extension-cli] <- OK"
		if output != "" {
			message += " " + output
		}
		state.appendCLIOutput(message)
	}
}

func runCLIButton(state *appState, button *widget.Button, label string, args ...string) {
	runCLIButtonWhenReady(state, button, label, "", nil, args...)
}

func runCLIButtonWhenReady(state *appState, button *widget.Button, label, successMessage string, ready func() bool, args ...string) {
	button.Disable()
	setCommandButtonResult(button, 0)
	state.clearCLIOutput()
	state.appendCLIOutput("[Request from AutoIPO app] -> " + formatCLICommand(args...))
	go func() {
		output, err := state.extensionCLI.Run(args...)
		fyne.Do(func() {
			appendCLIResult(state, args, output, err)
			if err != nil {
				setCommandButtonResult(button, -1)
			} else {
				setCommandButtonResult(button, 1)
			}
			defer func() {
				if ready == nil || ready() {
					button.Enable()
				} else {
					button.Disable()
				}
			}()
			if err != nil {
				return
			}
			if successMessage != "" {
				state.appendCLIOutput(successMessage)
			} else if output == "" {
				state.appendCLIOutput(label + " completed.")
			}
		})
	}()
}

func normalizeExtensionWebsiteURL(raw string) (string, error) {
	websiteURL := strings.TrimSpace(raw)
	if websiteURL == "" {
		return "", fmt.Errorf("enter a website address")
	}
	if !strings.Contains(websiteURL, "://") {
		websiteURL = "https://" + websiteURL
	}
	parsed, err := url.ParseRequestURI(websiteURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("enter a valid http:// or https:// website address")
	}
	return parsed.String(), nil
}

type cliOption struct {
	Display string
	Label   string
	Value   string
}

var cliOptionLine = regexp.MustCompile(`^\d+\.\s*(.*)$`)

func parseCLIOptions(output string) []cliOption {
	var options []cliOption
	for _, line := range strings.Split(output, "\n") {
		match := cliOptionLine.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 2 || match[1] == "" {
			continue
		}
		display := match[1]
		option := cliOption{Display: display, Label: display}
		if open := strings.LastIndex(display, " ["); open >= 0 && strings.HasSuffix(display, "]") {
			option.Label = display[:open]
			option.Value = display[open+2 : len(display)-1]
		}
		options = append(options, option)
	}
	return options
}

func selectedOptionValue(display string) string {
	for _, option := range parseCLIOptions("1. " + display) {
		if option.Value != "" {
			return option.Value
		}
		return option.Label
	}
	return display
}
