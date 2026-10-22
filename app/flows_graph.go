package main

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

type graphFlow struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	RunsToKeep   int               `json:"runs_to_keep"`
	Placeholders map[string]string `json:"placeholders,omitempty"`
	StartID      string            `json:"start_id"`
	SuccessID    string            `json:"success_id"`
	TerminalID   string            `json:"terminal_id"`
	Nodes        []graphNode       `json:"nodes"`
}

type graphNode struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Name       string   `json:"name,omitempty"`
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	NextID     string   `json:"next_id,omitempty"`
	Expression string   `json:"expression,omitempty"`
	Retries    int      `json:"retries,omitempty"`
	SuccessID  string   `json:"success_id,omitempty"`
	FallbackID string   `json:"fallback_id,omitempty"`
	Placeholders map[string]string `json:"placeholders,omitempty"`
	PlaceholderText string `json:"placeholder_text,omitempty"`
}

func graphStorePath() string { return filepath.Join(appDataDir(), "flows.json") }

func loadGraphFlows() ([]graphFlow, error) {
	data, err := os.ReadFile(graphStorePath())
	if os.IsNotExist(err) { return []graphFlow{}, nil }
	if err != nil { return nil, err }
	var flows []graphFlow
	if err := json.Unmarshal(data, &flows); err != nil { return nil, err }
	if flows == nil { flows = []graphFlow{} }
	// The old step-based file is intentionally not migrated: users asked to
	// restart with the graph flow model, and its nodes have different semantics.
	graphFlows := flows[:0]
	for _, flow := range flows {
		if validateGraphFlow(flow) != nil { continue }
		if startIndex := graphNodeIndex(&flow, flow.StartID); startIndex >= 0 {
			start := &flow.Nodes[startIndex]
			if start.PlaceholderText == "" && len(start.Placeholders) > 0 {
				start.PlaceholderText = formatFlowPlaceholders(start.Placeholders)
			}
		}
		if len(flow.Placeholders) > 0 {
			if startIndex := graphNodeIndex(&flow, flow.StartID); startIndex >= 0 && len(flow.Nodes[startIndex].Placeholders) == 0 {
				flow.Nodes[startIndex].Placeholders = flow.Placeholders
				flow.Nodes[startIndex].PlaceholderText = formatFlowPlaceholders(flow.Placeholders)
			}
			flow.Placeholders = nil
		}
		if flow.RunsToKeep < 1 { flow.RunsToKeep = 10 }
		graphFlows = append(graphFlows, flow)
	}
	return graphFlows, nil
}

func saveGraphFlows(flows []graphFlow) error {
	if err := os.MkdirAll(appDataDir(), 0o700); err != nil { return err }
	for _, flow := range flows { if err := validateGraphFlow(flow); err != nil { return fmt.Errorf("flow %q: %w", flow.Name, err) } }
	data, err := json.MarshalIndent(flows, "", "  ")
	if err != nil { return err }
	path := graphStorePath()
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil { return err }
	if err := os.Rename(path+".tmp", path); err != nil { _ = os.Remove(path+".tmp"); return err }
	return nil
}

func validateGraphFlow(flow graphFlow) error {
	if flow.StartID == "" || flow.SuccessID == "" || flow.TerminalID == "" { return fmt.Errorf("Start, Success, and Terminal states are required") }
	ids := make(map[string]string, len(flow.Nodes))
	counts := map[string]int{}
	for _, node := range flow.Nodes {
		if node.ID == "" { return fmt.Errorf("every state needs a unique ID") }
		if _, exists := ids[node.ID]; exists { return fmt.Errorf("duplicate state ID %q", node.ID) }
		ids[node.ID] = node.Type
		if node.Type == "start" || node.Type == "success" || node.Type == "terminal" { counts[node.Type]++ }
		if node.Type != "start" && node.Type != "command" && node.Type != "qualifier" && node.Type != "success" && node.Type != "terminal" { return fmt.Errorf("state %q has unknown type %q", node.ID, node.Type) }
	}
	if counts["start"] != 1 || counts["success"] != 1 || counts["terminal"] != 1 { return fmt.Errorf("flow must have exactly one Start, Success, and Terminal state") }
	for _, required := range []struct{ id, kind string }{{flow.StartID,"start"}, {flow.SuccessID,"success"}, {flow.TerminalID,"terminal"}} {
		if ids[required.id] != required.kind { return fmt.Errorf("flow must contain its required %s state", required.kind) }
	}
	return nil
}

func duplicateGraphFlow(flow graphFlow, name string) graphFlow {
	copy, _ := cloneGraphFlow(flow)
	copy.ID = newFlowID()
	copy.Name = name
	remap := make(map[string]string, len(copy.Nodes))
	for i := range copy.Nodes { remap[copy.Nodes[i].ID] = newFlowID() }
	for i := range copy.Nodes {
		node := &copy.Nodes[i]
		node.ID = remap[node.ID]
		if node.NextID != "" { node.NextID = remap[node.NextID] }
		if node.SuccessID != "" { node.SuccessID = remap[node.SuccessID] }
		if node.FallbackID != "" { node.FallbackID = remap[node.FallbackID] }
	}
	copy.StartID, copy.SuccessID, copy.TerminalID = remap[copy.StartID], remap[copy.SuccessID], remap[copy.TerminalID]
	return copy
}

func newGraphFlow(name string) graphFlow {
	start, success, terminal := newFlowID(), newFlowID(), newFlowID()
	return graphFlow{
		ID: newFlowID(), Name: name, RunsToKeep: 10, Placeholders: map[string]string{},
		StartID: start, SuccessID: success, TerminalID: terminal,
		Nodes: []graphNode{{ID:start, Type:"start", Name:"Start", Placeholders: map[string]string{}} , {ID:success, Type:"success", Name:"Success"}, {ID:terminal, Type:"terminal", Name:"Terminal"}},
	}
}

func graphNodeLabel(node graphNode) string {
	name := strings.TrimSpace(node.Name)
	if name == "" { name = node.Type }
	if node.Type == "command" && node.Command != "" { name += " · " + node.Command }
	shortID := node.ID
	if len(shortID) > 8 { shortID = shortID[:8] }
	return name + "  [" + shortID + "]"
}

func graphNodeIndex(flow *graphFlow, id string) int {
	if flow == nil { return -1 }
	for i := range flow.Nodes { if flow.Nodes[i].ID == id { return i } }
	return -1
}

func graphTargetLabel(flow *graphFlow, id string) string {
	if id == "" { return "(terminal by default)" }
	index := graphNodeIndex(flow, id)
	if index < 0 { return "(terminal by default)" }
	return graphNodeLabel(flow.Nodes[index])
}

func graphTransitionSummary(flow graphFlow, node graphNode) string {
	switch node.Type {
	case "start":
		return graphNodeLabel(node) + " → " + graphTargetLabel(&flow, node.NextID)
	case "command":
		return graphNodeLabel(node) + " → " + graphTargetLabel(&flow, node.NextID)
	case "qualifier":
		return graphNodeLabel(node) + " → true: " + graphTargetLabel(&flow, node.SuccessID) + " · false after retries: " + graphTargetLabel(&flow, node.FallbackID)
	default:
		return graphNodeLabel(node)
	}
}

func graphCommandArgCount(command string) int {
	switch command {
	case "set-url", "click", "dropdown", "page-contains", "random-sleep": return 1
	case "fill", "choose": return 2
	default: return 0
	}
}

func graphCommandArgPlaceholders(command string) []string {
	switch command {
	case "set-url": return []string{"URL or @name"}
	case "click", "dropdown", "page-contains": return []string{"CSS selector or @name"}
	case "fill": return []string{"CSS selector or @name", "Text or @name"}
	case "choose": return []string{"CSS selector or @name", "Option value or @name"}
	case "random-sleep": return []string{"Maximum seconds or @name"}
	default: return nil
	}
}

func cloneGraphFlow(flow graphFlow) (graphFlow, error) {
	data, err := json.Marshal(flow)
	if err != nil { return graphFlow{}, err }
	var copy graphFlow
	err = json.Unmarshal(data, &copy)
	return copy, err
}

func newFlowsPanel(state *appState) fyne.CanvasObject {
	flows, loadErr := loadGraphFlows()
	activeFlowID, selectedNodeID := "", ""
	playing := false
	zoomScale := float32(1)
	var flowCards *fyne.Container
	var pages *fyne.Container
	var listPage, builderPage fyne.CanvasObject
	var showPage func(fyne.CanvasObject)
	var refreshFlowCards func()
	var refreshNodeList func()
	var fillFlowEditor func(graphFlow)
	var fillNodeEditor func(string)
	var updateNodeActions func()
	var graphView *flowGraphCanvas
	var editNodeButton, saveNodeButton, cancelNodeButton *widget.Button
	var addButton *widget.Button
	var playButton *widget.Button
	var saveButton *widget.Button
	var selectedNode *graphNode
	var nodeEditSnapshot graphNode
	var editingNode, loadingNode bool
	var runLog *os.File
	var runMu sync.Mutex
	var completeRun func()

	logRun := func(line string) {
		runMu.Lock()
		if runLog != nil { _, _ = fmt.Fprintf(runLog, "%s %s\n", time.Now().Format(time.RFC3339), line) }
		runMu.Unlock()
	}

	flowName := widget.NewEntry()
	flowName.SetPlaceHolder("Flow name")
	startValues := widget.NewMultiLineEntry()
	startValues.SetPlaceHolder("@username=myusername\n@account=12345")
	startValues.SetMinRowsVisible(3)
	startValues.Wrapping = fyne.TextWrapWord
	keepLogs := widget.NewEntry()
	keepLogs.SetPlaceHolder("10")
	nodeKind := widget.NewSelect([]string{"command", "qualifier"}, nil)
	nodeKind.SetSelected("command")
	changingNodeKind := false
	commandSelect := widget.NewSelect(flowCommands, nil)
	commandSelect.SetSelected("open")
	nodeName := widget.NewEntry()
	nodeType := widget.NewLabel("Select a state")
	nodeNameLabel, nodeKindLabel := widget.NewLabel("State name"), widget.NewLabel("State type")
	commandLabel, arg1Label, arg2Label := widget.NewLabel("Command"), widget.NewLabel("Argument 1"), widget.NewLabel("Argument 2")
	expressionLabel, retriesLabel := widget.NewLabel("Qualifier expression"), widget.NewLabel("Retries")
	startValuesLabel, nextTargetLabel := widget.NewLabel("Start values (@name=value)"), widget.NewLabel("Next state")
	successTargetLabel, fallbackTargetLabel := widget.NewLabel("True target"), widget.NewLabel("False fallback")
	arg1, arg2 := widget.NewEntry(), widget.NewEntry()
	arg1.SetPlaceHolder("Argument 1")
	arg2.SetPlaceHolder("Argument 2")
	expression := widget.NewMultiLineEntry()
	expression.SetPlaceHolder(`pageContains("#kc-login") AND pageContains("#username")`)
	expression.SetMinRowsVisible(3)
	retries := widget.NewEntry()
	retries.SetPlaceHolder("0")
	nextTarget := widget.NewSelect([]string{"(terminal by default)"}, nil)
	successTarget := widget.NewSelect([]string{"(terminal by default)"}, nil)
	fallbackTarget := widget.NewSelect([]string{"(terminal by default)"}, nil)
	targetIDs := map[string]string{"(terminal by default)": ""}
	deleteNodeButton := widget.NewButton("Delete", nil)
	deleteNodeButton.Disable()
	editNodeButton = widget.NewButton("Edit", nil)
	saveNodeButton = widget.NewButton("Save state", nil)
	cancelNodeButton = widget.NewButton("Cancel", nil)
	saveNodeButton.Disable(); cancelNodeButton.Disable()
	var deleteSelectedNode func()
	insertRoute := widget.NewSelect([]string{"Next"}, nil)
	insertButton := widget.NewButton("Insert after state", nil)
	insertButton.Disable()

	flowByID := func(id string) *graphFlow {
		for i := range flows { if flows[i].ID == id { return &flows[i] } }
		return nil
	}
	activeFlow := func() *graphFlow { return flowByID(activeFlowID) }
	selectTargetID := func(target *widget.Select) string { return targetIDs[target.Selected] }
	setTarget := func(target *widget.Select, id string) { target.SetSelected(graphTargetLabel(activeFlow(), id)) }
	setNodeEditorEnabled := func(enabled bool) {
		for _, object := range []fyne.CanvasObject{nodeName, nodeKind, commandSelect, arg1, arg2, expression, retries, startValues, nextTarget, successTarget, fallbackTarget} {
			if control, ok := object.(interface{ Enable(); Disable() }); ok {
				if enabled { control.Enable() } else { control.Disable() }
			}
		}
	}
	refreshNodeList = func() {
		flow := activeFlow()
		options := []string{"(terminal by default)"}
		targetIDs = map[string]string{"(terminal by default)": ""}
		if flow != nil {
			for _, node := range flow.Nodes { label := graphNodeLabel(node); options = append(options, label); targetIDs[label] = node.ID }
		}
		for _, target := range []*widget.Select{nextTarget, successTarget, fallbackTarget} {
			previous := target.Selected
			target.Options = options
			target.Refresh()
			if _, exists := targetIDs[previous]; exists { target.SetSelected(previous) } else { target.SetSelected(options[0]) }
		}
		if graphView != nil { refreshGraphCanvas(graphView, activeFlow(), selectedNodeID, zoomScale, func(id string) { if !editingNode || id != selectedNodeID { fillNodeEditor(id) } }) }
	}
	graphView = newFlowGraphCanvas(state.window)
	fillNodeEditor = func(id string) {
		if editingNode && id != selectedNodeID {
			dialog.ShowConfirm("Discard state edits?", "Save or discard the current state edits before selecting another state.", func(ok bool) {
				if !ok { return }
				if flow := activeFlow(); flow != nil { if index := graphNodeIndex(flow, selectedNodeID); index >= 0 { flow.Nodes[index] = nodeEditSnapshot } }
				editingNode = false
				refreshNodeList()
				fillNodeEditor(id)
			}, state.window)
			return
		}
		editingNode = false
		selectedNodeID = id
		flow := activeFlow()
		selectedNode = nil
		if flow != nil { if index := graphNodeIndex(flow, id); index >= 0 { selectedNode = &flow.Nodes[index] } }
		for _, object := range []fyne.CanvasObject{nodeNameLabel, nodeName, nodeKindLabel, nodeKind, commandLabel, commandSelect, arg1Label, arg1, arg2Label, arg2, expressionLabel, expression, retriesLabel, retries, startValuesLabel, startValues, nextTargetLabel, nextTarget, successTargetLabel, successTarget, fallbackTargetLabel, fallbackTarget} {
			object.Hide()
			if control, ok := object.(interface{ Enable(); Disable() }); ok { control.Disable() }
		}
		deleteNodeButton.Disable()
		editNodeButton.Disable(); saveNodeButton.Disable(); cancelNodeButton.Disable()
		editNodeButton.Hide(); saveNodeButton.Hide(); cancelNodeButton.Hide(); deleteNodeButton.Hide()
		if addButton != nil { addButton.Enable() }
		setNodeEditorEnabled(false)
		insertButton.Disable()
		if selectedNode == nil { nodeType.SetText("Select a state"); updateNodeActions(); return }
		editNodeButton.Show()
		loadingNode = true
		nodeType.SetText(strings.ToUpper(selectedNode.Type))
		if selectedNode.Type == "command" || selectedNode.Type == "qualifier" { nodeNameLabel.Show(); nodeName.Show(); nodeName.SetText(selectedNode.Name); nodeKindLabel.Show(); nodeKind.Show(); changingNodeKind = true; nodeKind.SetSelected(selectedNode.Type); changingNodeKind = false }
		switch selectedNode.Type {
		case "start":
			startValuesLabel.Show(); startValues.Show(); startValues.SetText(selectedNode.PlaceholderText)
			nextTargetLabel.Show(); nextTarget.Show(); setTarget(nextTarget, selectedNode.NextID)
		case "command":
			commandLabel.Show(); commandSelect.Show(); commandSelect.SetSelected(selectedNode.Command)
			setGraphCommandPlaceholders(commandSelect.Selected, arg1, arg2)
			arg1.SetText(""); arg2.SetText("")
			argCount := graphCommandArgCount(selectedNode.Command)
			if argCount > 0 { arg1Label.Show(); arg1.Show(); if len(selectedNode.Args) > 0 { arg1.SetText(selectedNode.Args[0]) } }
			if argCount > 1 { arg2Label.Show(); arg2.Show(); if len(selectedNode.Args) > 1 { arg2.SetText(selectedNode.Args[1]) } }
			nextTargetLabel.Show(); nextTarget.Show(); setTarget(nextTarget, selectedNode.NextID); deleteNodeButton.Enable()
		case "qualifier":
			expressionLabel.Show(); expression.Show(); expression.SetText(selectedNode.Expression)
			retriesLabel.Show(); retries.Show(); retries.SetText(strconv.Itoa(selectedNode.Retries))
			successTargetLabel.Show(); successTarget.Show(); setTarget(successTarget, selectedNode.SuccessID)
			fallbackTargetLabel.Show(); fallbackTarget.Show(); setTarget(fallbackTarget, selectedNode.FallbackID); deleteNodeButton.Enable()
		}
		loadingNode = false
		if selectedNode.Type != "success" && selectedNode.Type != "terminal" { editNodeButton.Enable() }
		if selectedNode.Type == "command" || selectedNode.Type == "qualifier" { deleteNodeButton.Show(); deleteNodeButton.Enable() }
		updateNodeActions()
		if graphView != nil { refreshGraphCanvas(graphView, activeFlow(), selectedNodeID, zoomScale, func(id string) { if !editingNode || id != selectedNodeID { fillNodeEditor(id) } }) }
	}
	updateNodeActions = func() {
		if selectedNode == nil { insertRoute.Options = []string{"Select a state"}; insertRoute.SetSelected("Select a state"); insertRoute.Disable(); insertButton.Disable(); return }
		switch selectedNode.Type {
		case "start", "command":
			insertRoute.Options = []string{"Next"}; insertRoute.Enable(); insertRoute.SetSelected("Next"); insertButton.Enable()
		case "qualifier":
			insertRoute.Options = []string{"True path", "False fallback"}; insertRoute.Enable(); insertRoute.SetSelected("True path"); insertButton.Enable()
		default:
			insertRoute.Options = []string{"No outgoing connection"}; insertRoute.SetSelected("No outgoing connection"); insertRoute.Disable(); insertButton.Disable()
		}
		insertRoute.Refresh()
	}
	beginNodeEdit := func() {
		if selectedNode == nil || selectedNode.Type == "success" || selectedNode.Type == "terminal" { return }
		editingNode = true
		setNodeEditorEnabled(true)
		editNodeButton.Disable(); saveNodeButton.Enable(); cancelNodeButton.Enable()
		editNodeButton.Hide(); saveNodeButton.Show(); cancelNodeButton.Show()
		insertButton.Disable(); insertRoute.Disable()
		if addButton != nil { addButton.Disable() }
	}
	nodeKind.OnChanged = func(kind string) {
		if loadingNode || !editingNode || changingNodeKind || selectedNode == nil || (kind != "command" && kind != "qualifier") || selectedNode.Type == kind { return }
		if kind == "qualifier" {
			selectedNode.Type = "qualifier"
			selectedNode.SuccessID = selectedNode.NextID
			selectedNode.NextID = ""
			selectedNode.Command, selectedNode.Args = "", nil
			selectedNode.Expression, selectedNode.Retries, selectedNode.FallbackID = "", 0, ""
		} else {
			selectedNode.Type = "command"
			selectedNode.NextID = selectedNode.SuccessID
			if selectedNode.NextID == "" { selectedNode.NextID = selectedNode.FallbackID }
			selectedNode.Command, selectedNode.Args = "open", nil
			selectedNode.Expression, selectedNode.Retries, selectedNode.SuccessID, selectedNode.FallbackID = "", 0, "", ""
		}
		id := selectedNode.ID
		refreshNodeList()
		fillNodeEditor(id)
		beginNodeEdit()
	}
	commandSelect.OnChanged = func(command string) {
		count := graphCommandArgCount(command)
		setGraphCommandPlaceholders(command, arg1, arg2)
		if count > 0 { arg1.Show() } else { arg1.Hide() }
		if count > 1 { arg2.Show() } else { arg2.Hide() }
		if count > 0 { arg1Label.Show() } else { arg1Label.Hide() }
		if count > 1 { arg2Label.Show() } else { arg2Label.Hide() }
		if selectedNode != nil && selectedNode.Type == "command" {
			if !loadingNode && editingNode {
				selectedNode.Command = command
				for len(selectedNode.Args) < count { selectedNode.Args = append(selectedNode.Args, "") }
				if len(selectedNode.Args) > count { selectedNode.Args = selectedNode.Args[:count] }
			}
			if editingNode && graphView != nil { refreshGraphCanvas(graphView, activeFlow(), selectedNodeID, zoomScale, func(id string) { if !editingNode || id != selectedNodeID { fillNodeEditor(id) } }) }
		}
	}
	refreshSelectedGraph := func() { if editingNode && graphView != nil { refreshGraphCanvas(graphView, activeFlow(), selectedNodeID, zoomScale, func(id string) { if !editingNode || id != selectedNodeID { fillNodeEditor(id) } }) } }
	nodeName.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && (selectedNode.Type == "command" || selectedNode.Type == "qualifier") { selectedNode.Name = value; refreshSelectedGraph() } }
	startValues.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "start" { selectedNode.PlaceholderText = value; refreshSelectedGraph() } }
	arg1.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "command" && len(selectedNode.Args) > 0 { selectedNode.Args[0] = value; refreshSelectedGraph() } }
	arg2.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "command" && len(selectedNode.Args) > 1 { selectedNode.Args[1] = value; refreshSelectedGraph() } }
	expression.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "qualifier" { selectedNode.Expression = value; refreshSelectedGraph() } }
	retries.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "qualifier" { if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 0 { selectedNode.Retries = parsed; refreshSelectedGraph() } } }
	nextTarget.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && (selectedNode.Type == "start" || selectedNode.Type == "command") { selectedNode.NextID = targetIDs[value]; refreshSelectedGraph() } }
	successTarget.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "qualifier" { selectedNode.SuccessID = targetIDs[value]; refreshSelectedGraph() } }
	fallbackTarget.OnChanged = func(value string) { if !loadingNode && editingNode && selectedNode != nil && selectedNode.Type == "qualifier" { selectedNode.FallbackID = targetIDs[value]; refreshSelectedGraph() } }

	addButton = widget.NewButton("Add state", func() {
		flow := activeFlow(); if flow == nil { return }
		node := graphNode{ID:newFlowID(), Type:"command", Name:"Command", Command:"open"}
		selectedNode = nil
		flow.Nodes = append(flow.Nodes, node)
		refreshNodeList(); fillNodeEditor(node.ID)
		nodeEditSnapshot = *selectedNode; beginNodeEdit()
	})
	insertButton.OnTapped = func() {
		flow := activeFlow(); if flow == nil || selectedNode == nil { return }
		index := graphNodeIndex(flow, selectedNode.ID); if index < 0 { return }
		newNode := graphNode{ID:newFlowID(), Type:"command", Name:"Command", Command:"open"}
		var previousTarget string
		switch flow.Nodes[index].Type {
		case "start", "command":
			previousTarget = flow.Nodes[index].NextID
			flow.Nodes[index].NextID = newNode.ID
		case "qualifier":
			if insertRoute.Selected == "False fallback" { previousTarget = flow.Nodes[index].FallbackID; flow.Nodes[index].FallbackID = newNode.ID
			} else { previousTarget = flow.Nodes[index].SuccessID; flow.Nodes[index].SuccessID = newNode.ID }
		default: return
		}
		newNode.NextID = previousTarget
		flow.Nodes = append(flow.Nodes, graphNode{})
		copy(flow.Nodes[index+2:], flow.Nodes[index+1:])
		flow.Nodes[index+1] = newNode
		selectedNode = nil
		refreshNodeList(); fillNodeEditor(newNode.ID)
		nodeEditSnapshot = *selectedNode; beginNodeEdit()
	}
	deleteNodeButton.OnTapped = func() {
		flow := activeFlow(); if flow == nil || selectedNode == nil || selectedNode.ID == flow.StartID || selectedNode.ID == flow.SuccessID || selectedNode.ID == flow.TerminalID { return }
		dialog.ShowConfirm("Delete state", "Delete this state? Incoming connections will route to Terminal by default.", func(ok bool) { if ok { deleteSelectedNode() } }, state.window)
	}
	deleteSelectedNode = func() {
		flow := activeFlow(); if flow == nil || selectedNode == nil || selectedNode.ID == flow.StartID || selectedNode.ID == flow.SuccessID || selectedNode.ID == flow.TerminalID { return }
		removedID := selectedNode.ID
		for i := range flow.Nodes { if flow.Nodes[i].ID == removedID { flow.Nodes = append(flow.Nodes[:i], flow.Nodes[i+1:]...); break } }
		for i := range flow.Nodes {
			if flow.Nodes[i].NextID == removedID { flow.Nodes[i].NextID = "" }
			if flow.Nodes[i].SuccessID == removedID { flow.Nodes[i].SuccessID = "" }
			if flow.Nodes[i].FallbackID == removedID { flow.Nodes[i].FallbackID = "" }
		}
		selectedNode = nil; selectedNodeID = ""; refreshNodeList(); fillNodeEditor("")
	}

	saveEditor := func() error {
		flow := activeFlow(); if flow == nil { return fmt.Errorf("create or select a flow first") }
		flow.Name = strings.TrimSpace(flowName.Text); if flow.Name == "" { return fmt.Errorf("enter a flow name") }
		for _, other := range flows { if other.ID != flow.ID && strings.EqualFold(strings.TrimSpace(other.Name), flow.Name) { return fmt.Errorf("another flow already uses that name") } }
		count, err := strconv.Atoi(strings.TrimSpace(keepLogs.Text)); if err != nil || count < 1 { return fmt.Errorf("run log retention must be at least 1") }; flow.RunsToKeep = count
		flow.Placeholders = nil
		if startIndex := graphNodeIndex(flow, flow.StartID); startIndex >= 0 {
			startNode := &flow.Nodes[startIndex]
			values, err := parseFlowPlaceholders(startNode.PlaceholderText); if err != nil { return err }
			startNode.Placeholders = values
		}
		if selectedNode != nil && editingNode {
			if index := graphNodeIndex(flow, selectedNodeID); index >= 0 {
				node := &flow.Nodes[index]
				switch node.Type {
				case "start": node.NextID = selectTargetID(nextTarget)
				case "command":
					node.Name, node.Command = strings.TrimSpace(nodeName.Text), commandSelect.Selected
					count := graphCommandArgCount(node.Command); node.Args = nil
					if count > 0 { node.Args = append(node.Args, arg1.Text) }; if count > 1 { node.Args = append(node.Args, arg2.Text) }
					node.NextID = selectTargetID(nextTarget)
				case "qualifier":
					node.Name, node.Expression = strings.TrimSpace(nodeName.Text), strings.TrimSpace(expression.Text)
					node.Retries, err = strconv.Atoi(strings.TrimSpace(retries.Text)); if err != nil || node.Retries < 0 { return fmt.Errorf("qualifier retries must be an integer of 0 or greater") }
					node.SuccessID, node.FallbackID = selectTargetID(successTarget), selectTargetID(fallbackTarget)
				}
			}
		}
		return nil
	}
	editNodeButton.OnTapped = func() {
		if selectedNode == nil || selectedNode.Type == "success" || selectedNode.Type == "terminal" { return }
		nodeEditSnapshot = *selectedNode
		beginNodeEdit()
	}
	saveNodeButton.OnTapped = func() {
		if !editingNode { return }
		if err := saveEditor(); err != nil { dialog.ShowError(err, state.window); return }
		if err := saveGraphFlows(flows); err != nil { dialog.ShowError(err, state.window); return }
		id := selectedNodeID
		refreshFlowCards()
		fillNodeEditor(id)
	}
	cancelNodeButton.OnTapped = func() {
		if !editingNode || selectedNode == nil { return }
		if flow := activeFlow(); flow != nil { if index := graphNodeIndex(flow, selectedNodeID); index >= 0 { flow.Nodes[index] = nodeEditSnapshot } }
		fillNodeEditor(selectedNodeID)
	}
	 saveButton = widget.NewButton("Save flow", func() {
		if err := saveEditor(); err != nil { dialog.ShowError(err, state.window); return }
		if err := saveGraphFlows(flows); err != nil { dialog.ShowError(err, state.window); return }
		refreshFlowCards(); showPage(listPage)
	})

	flowName.OnChanged = func(string) { if flow := activeFlow(); flow != nil { flow.Name = flowName.Text } }
	newButton := widget.NewButton("New flow", func() {
		name := "New flow"
		for n := 2; ; n++ { exists := false; for _, flow := range flows { if strings.EqualFold(flow.Name, name) { exists = true; break } }; if !exists { break }; name = fmt.Sprintf("New flow %d", n) }
		flow := newGraphFlow(name); flows = append(flows, flow); activeFlowID = flow.ID; selectedNode = nil; selectedNodeID = ""
		flowName.SetText(flow.Name); startValues.SetText(""); keepLogs.SetText("10")
		refreshNodeList(); fillNodeEditor(flow.StartID); showPage(builderPage)
	})

	playButton = widget.NewButton("Play flow", nil)
	playButton.OnTapped = func() {
		flow := activeFlow(); if flow == nil || playing { return }
		if err := saveEditor(); err != nil { dialog.ShowError(err, state.window); return }
		if err := saveGraphFlows(flows); err != nil { dialog.ShowError(err, state.window); return }
		snapshot, err := cloneGraphFlow(*flow); if err != nil { dialog.ShowError(err, state.window); return }
		start := func(values map[string]string) {
			playing = true; playButton.Disable(); playButton.SetText("Running…")
			runMu.Lock(); runLog = nil; runMu.Unlock()
			go func() {
				runID := newFlowID()
				if err := os.MkdirAll(flowRunsDir(snapshot.ID), 0o700); err != nil { logRun("[Terminal] cannot create run log directory: " + err.Error()); completeRun(); return }
				file, err := os.OpenFile(filepath.Join(flowRunsDir(snapshot.ID), runID+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil { logRun("[Terminal] cannot open run log: " + err.Error()); completeRun(); return }
				runMu.Lock(); runLog = file; runMu.Unlock()
				logRun("[Request from Flow runner] -> start " + strconv.Quote(snapshot.Name) + " run " + runID)
				executeFlowGraph(snapshot, values,
					func(args ...string) (string, error) { return state.extensionCLI.Run(args...) },
					func() error { return state.extensionCLI.StartServe(io.Discard) }, logRun)
				_ = file.Close(); runMu.Lock(); runLog = nil; runMu.Unlock()
				if err := pruneFlowRunLogs(snapshot.ID, snapshot.RunsToKeep); err != nil { logRun("[warning] could not prune old run logs: " + err.Error()) }
				completeRun()
			}()
		}
		var names []string
		seen := map[string]bool{}
		for _, node := range snapshot.Nodes { for _, arg := range node.Args { addPlaceholderNames(arg, seen, &names) }; addPlaceholderNames(node.Expression, seen, &names) }
		sort.Strings(names)
		if len(names) == 0 { start(map[string]string{}); return }
		entries := make(map[string]*widget.Entry, len(names)); items := make([]*widget.FormItem, 0, len(names))
		startPlaceholders := map[string]string{}
		if startIndex := graphNodeIndex(&snapshot, snapshot.StartID); startIndex >= 0 { startPlaceholders = snapshot.Nodes[startIndex].Placeholders }
		for _, name := range names { entry := widget.NewEntry(); entry.SetText(startPlaceholders[name]); entry.SetPlaceHolder("Value for " + name); entries[name] = entry; items = append(items, &widget.FormItem{Text:name, Widget:entry}) }
		dialog.ShowForm("Flow values", "Start flow", "Cancel", items, func(ok bool) { if !ok { return }; values := make(map[string]string, len(entries)); for name, entry := range entries { values[name] = entry.Text }; start(values) }, state.window)
	}
	completeRun = func() {
		fyne.Do(func() { playing = false; playButton.SetText("Play flow"); if activeFlow() != nil { playButton.Enable() } })
	}

	stateControls := container.NewHBox(editNodeButton, saveNodeButton, cancelNodeButton, deleteNodeButton)
	stateEditor := container.NewVBox(nodeType, stateControls, nodeNameLabel, nodeName, nodeKindLabel, nodeKind,
		commandLabel, commandSelect, arg1Label, arg1, arg2Label, arg2,
		expressionLabel, expression, retriesLabel, retries,
		startValuesLabel, startValues, nextTargetLabel, nextTarget, successTargetLabel, successTarget, fallbackTargetLabel, fallbackTarget)
	stateSplit := container.NewHSplit(container.NewScroll(graphView), container.NewScroll(stateEditor)); stateSplit.SetOffset(0.68)
	backButton := widget.NewButton("Back to flows", func() { showPage(listPage) })
	zoomLabel := widget.NewLabel("100%")
	zoomOut := widget.NewButton("−", func() { zoomScale *= 0.8; if zoomScale < 0.15 { zoomScale = 0.15 }; zoomLabel.SetText(fmt.Sprintf("%.0f%%", zoomScale*100)); refreshNodeList() })
	zoomIn := widget.NewButton("+", func() { zoomScale *= 1.25; if zoomScale > 2.5 { zoomScale = 2.5 }; zoomLabel.SetText(fmt.Sprintf("%.0f%%", zoomScale*100)); refreshNodeList() })
	zoomFit := widget.NewButton("Fit graph", func() { zoomScale = graphFitZoom(activeFlow(), graphView.viewportSize()); zoomLabel.SetText(fmt.Sprintf("%.0f%%", zoomScale*100)); refreshNodeList() })
	zoomReset := widget.NewButton("100%", func() { zoomScale = 1; zoomLabel.SetText("100%"); refreshNodeList() })
	graphEditor := container.NewBorder(
		container.NewVBox(container.NewHBox(backButton, flowName, layout.NewSpacer(), saveButton), container.NewHBox(widget.NewLabel("Keep latest run logs"), keepLogs, addButton)),
		container.NewHBox(insertRoute, insertButton, layout.NewSpacer(), zoomOut, zoomLabel, zoomIn, zoomFit, zoomReset), nil, nil, stateSplit)
	builderPage = graphEditor
	flowCards = container.NewVBox()
	listPage = container.NewBorder(container.NewBorder(nil, nil, nil, newButton, widget.NewLabel("Flows")), nil, nil, nil, container.NewScroll(flowCards))
	refreshFlowCards = func() {
		flowCards.Objects = nil
		for _, saved := range flows {
			flow := saved
			var transitions []string
			for _, node := range flow.Nodes { transitions = append(transitions, graphTransitionSummary(flow, node)) }
			buttons := container.NewHBox(
				widget.NewButton("Play", func() { activeFlowID = flow.ID; fillFlowEditor(flow); playButton.OnTapped() }),
				widget.NewButton("Edit", func() { fillFlowEditor(flow); showPage(builderPage) }),
				widget.NewButton("Run logs", func() { showPage(newFlowRunLogsPage(flow.ID, flow.Name, func() { showPage(listPage) })) }),
				widget.NewButton("Duplicate", func() {
					base := "Copy of " + flow.Name
					name := base
					for suffix := 2; ; suffix++ { exists := false; for _, other := range flows { if strings.EqualFold(other.Name, name) { exists = true; break } }; if !exists { break }; name = fmt.Sprintf("%s %d", base, suffix) }
					copy := duplicateGraphFlow(flow, name)
					flows = append(flows, copy)
					if err := saveGraphFlows(flows); err != nil { flows = flows[:len(flows)-1]; dialog.ShowError(err, state.window) }
					refreshFlowCards()
				}),
				widget.NewButton("Delete", func() { dialog.ShowConfirm("Delete flow", "Delete this flow and its run logs?", func(ok bool) { if !ok { return }; for i := range flows { if flows[i].ID == flow.ID { flows = append(flows[:i], flows[i+1:]...); break } }; _ = os.RemoveAll(filepath.Join(appDataDir(), "flows", flow.ID)); if err := saveGraphFlows(flows); err != nil { dialog.ShowError(err, state.window) }; refreshFlowCards() }, state.window) }),
			)
			chainLabel := widget.NewLabel(strings.Join(transitions, "\n")); chainLabel.Wrapping = fyne.TextWrapWord
			flowCards.Add(widget.NewCard(flow.Name, fmt.Sprintf("%d states · keep %d run logs", len(flow.Nodes), flow.RunsToKeep), container.NewVBox(chainLabel, buttons)))
		}
		if len(flows) == 0 { flowCards.Add(widget.NewLabel("No saved flows. Create one to get started.")) }
		flowCards.Refresh()
	}
	fillFlowEditor = func(flow graphFlow) {
		selectedNode = nil; selectedNodeID = ""; activeFlowID = flow.ID; flowName.SetText(flow.Name); keepLogs.SetText(strconv.Itoa(max(flow.RunsToKeep, 1)))
		zoomScale = graphFitZoom(&flow, graphView.viewportSize()); zoomLabel.SetText(fmt.Sprintf("%.0f%%", zoomScale*100))
		refreshNodeList(); fillNodeEditor(flow.StartID); playButton.Enable()
	}
	showPage = func(page fyne.CanvasObject) { pages.Objects = []fyne.CanvasObject{page}; pages.Refresh() }
	pages = container.NewStack(listPage)
	if loadErr != nil { dialog.ShowError(loadErr, state.window) }
	refreshNodeList(); refreshFlowCards()
	return pages
}

func setGraphCommandPlaceholders(command string, first, second *widget.Entry) {
	placeholders := graphCommandArgPlaceholders(command)
	if len(placeholders) > 0 { first.SetPlaceHolder(placeholders[0]) } else { first.SetPlaceHolder("Argument 1") }
	if len(placeholders) > 1 { second.SetPlaceHolder(placeholders[1]) } else { second.SetPlaceHolder("Argument 2") }
}

type flowGraphNodeWidget struct {
	widget.BaseWidget
	name string
	selected bool
	textSize float32
	onSelect func()
}

type flowGraphCanvas struct {
	widget.BaseWidget
	content *fyne.Container
	background *canvas.Rectangle
	window fyne.Window
	diagramSize fyne.Size
}

func newFlowGraphCanvas(window fyne.Window) *flowGraphCanvas {
	graph := &flowGraphCanvas{content: container.NewWithoutLayout(), window: window}
	graph.ExtendBaseWidget(graph)
	return graph
}

func (graph *flowGraphCanvas) viewportSize() fyne.Size {
	viewport := fyne.NewSize(720, 520)
	if graph.window != nil {
		windowSize := graph.window.Canvas().Size()
		if windowSize.Width > 0 { viewport.Width = max(windowSize.Width*0.62, float32(720)) }
		if windowSize.Height > 0 { viewport.Height = max(windowSize.Height-135, float32(520)) }
	}
	return viewport
}

func (graph *flowGraphCanvas) minContentSize() fyne.Size {
	viewport := graph.viewportSize()
	return fyne.NewSize(max(graph.diagramSize.Width, viewport.Width), max(graph.diagramSize.Height, viewport.Height))
}

func (graph *flowGraphCanvas) CreateRenderer() fyne.WidgetRenderer {
	return &flowGraphCanvasRenderer{graph: graph, objects: []fyne.CanvasObject{graph.content}}
}

type flowGraphCanvasRenderer struct {
	graph *flowGraphCanvas
	objects []fyne.CanvasObject
}

func (renderer *flowGraphCanvasRenderer) Layout(size fyne.Size) {
	renderer.graph.content.Resize(size)
	if renderer.graph.background != nil { renderer.graph.background.Resize(size) }
}
func (renderer *flowGraphCanvasRenderer) MinSize() fyne.Size { return renderer.graph.minContentSize() }
func (renderer *flowGraphCanvasRenderer) Refresh() { renderer.graph.content.Refresh() }
func (renderer *flowGraphCanvasRenderer) Objects() []fyne.CanvasObject { return renderer.objects }
func (renderer *flowGraphCanvasRenderer) Destroy() {}

func newFlowGraphNodeWidget(name string, selected bool, zoom float32, onSelect func()) *flowGraphNodeWidget {
	node := &flowGraphNodeWidget{name: name, selected: selected, textSize: 14*zoom, onSelect: onSelect}
	node.ExtendBaseWidget(node)
	return node
}

func (node *flowGraphNodeWidget) Tapped(*fyne.PointEvent) {
	if node.onSelect != nil { node.onSelect() }
}

func (node *flowGraphNodeWidget) CreateRenderer() fyne.WidgetRenderer {
	background := canvas.NewRectangle(color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	label := canvas.NewText(node.name, color.NRGBA{R: 39, G: 49, B: 62, A: 255})
	label.Alignment = fyne.TextAlignCenter
	label.TextStyle.Bold = true
	label.TextSize = node.textSize
	renderer := &flowGraphNodeRenderer{node: node, background: background, label: label, objects: []fyne.CanvasObject{background, label}}
	renderer.Refresh()
	return renderer
}

type flowGraphNodeRenderer struct {
	node *flowGraphNodeWidget
	background *canvas.Rectangle
	label *canvas.Text
	objects []fyne.CanvasObject
}

func (renderer *flowGraphNodeRenderer) Layout(size fyne.Size) {
	renderer.background.Resize(size)
	renderer.label.Resize(fyne.NewSize(size.Width-18, size.Height-10))
	renderer.label.Move(fyne.NewPos(9, 5))
}

func (renderer *flowGraphNodeRenderer) MinSize() fyne.Size { return fyne.NewSize(164, 56) }
func (renderer *flowGraphNodeRenderer) Refresh() {
	if renderer.node.selected {
		renderer.background.FillColor = color.NRGBA{R: 246, G: 196, B: 82, A: 255}
		renderer.background.StrokeColor = color.NRGBA{R: 178, G: 122, B: 25, A: 255}
		renderer.background.StrokeWidth = 2
	} else {
		renderer.background.FillColor = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
		renderer.background.StrokeColor = color.NRGBA{R: 178, G: 190, B: 205, A: 255}
		renderer.background.StrokeWidth = 1
	}
	renderer.label.Text = renderer.node.name
	renderer.label.TextSize = renderer.node.textSize
	renderer.background.Refresh(); renderer.label.Refresh()
}
func (renderer *flowGraphNodeRenderer) Objects() []fyne.CanvasObject { return renderer.objects }
func (renderer *flowGraphNodeRenderer) Destroy() {}

func graphFitZoom(flow *graphFlow, viewport fyne.Size) float32 {
	if flow == nil || len(flow.Nodes) == 0 { return 1 }
	nodes := make(map[string]graphNode, len(flow.Nodes))
	for _, node := range flow.Nodes { nodes[node.ID] = node }
	levels := map[string]int{flow.StartID: 0}
	queue := []string{flow.StartID}
	maxLevel := 0
	for len(queue) > 0 {
		id := queue[0]; queue = queue[1:]
		node, ok := nodes[id]; if !ok { continue }
		targets := []string{}
		switch node.Type {
		case "start", "command":
			target := node.NextID; if target == "" { target = flow.TerminalID }; targets = append(targets, target)
		case "qualifier":
			trueTarget, falseTarget := node.SuccessID, node.FallbackID
			if trueTarget == "" { trueTarget = flow.TerminalID }; if falseTarget == "" { falseTarget = flow.TerminalID }
			targets = append(targets, trueTarget, falseTarget)
		}
		for _, target := range targets {
			if _, exists := nodes[target]; !exists { continue }
			if _, seen := levels[target]; seen { continue }
			level := levels[id]+1; levels[target] = level; if level > maxLevel { maxLevel = level }; queue = append(queue, target)
		}
	}
	for _, node := range flow.Nodes { if _, seen := levels[node.ID]; !seen { maxLevel++; levels[node.ID] = maxLevel } }
	rows := make(map[int]int)
	for _, node := range flow.Nodes { rows[levels[node.ID]]++ }
	maxRows := 1; for _, count := range rows { if count > maxRows { maxRows = count } }
	baseWidth := float32(72+(maxLevel+1)*164+maxLevel*148)
	baseHeight := float32(72+maxRows*56+(maxRows-1)*48)
	zoom := min(viewport.Width/baseWidth, viewport.Height/baseHeight)
	if zoom > 1 { zoom = 1 }; if zoom < 0.15 { zoom = 0.15 }
	return zoom
}

func refreshGraphCanvas(view *flowGraphCanvas, flow *graphFlow, selectedID string, zoom float32, selectNode func(string)) {
	if view == nil || flow == nil { return }
	if zoom < 0.15 { zoom = 0.15 }; if zoom > 2.5 { zoom = 2.5 }
	const baseWidth, baseHeight = float32(164), float32(56)
	const baseHorizontalGap, baseVerticalGap, margin = float32(148), float32(48), float32(36)
	horizontalGap, verticalGap := baseHorizontalGap*zoom, baseVerticalGap*zoom
	nodeWidth, nodeHeight := baseWidth*zoom, baseHeight*zoom
	inset := margin*zoom
	nodes := make(map[string]graphNode, len(flow.Nodes))
	for _, node := range flow.Nodes { nodes[node.ID] = node }
	levels := map[string]int{flow.StartID: 0}
	queue := []string{flow.StartID}
	maxLevel := 0
	outgoing := func(node graphNode) []struct { id, label string; color color.Color } {
		blue := color.NRGBA{R: 75, G: 117, B: 169, A: 255}
		green := color.NRGBA{R: 44, G: 145, B: 93, A: 255}
		red := color.NRGBA{R: 193, G: 82, B: 82, A: 255}
		var edges []struct { id, label string; color color.Color }
		switch node.Type {
		case "start":
			target := node.NextID; if target == "" { target = flow.TerminalID; edges = append(edges, struct { id, label string; color color.Color }{target, "default", red}) } else { edges = append(edges, struct { id, label string; color color.Color }{target, "Next", blue}) }
		case "command":
			target := node.NextID; if target == "" { target = flow.TerminalID; edges = append(edges, struct { id, label string; color color.Color }{target, "default", red}) } else { edges = append(edges, struct { id, label string; color color.Color }{target, "Next", blue}) }
		case "qualifier":
			trueTarget := node.SuccessID; if trueTarget == "" { trueTarget = flow.TerminalID }
			falseTarget := node.FallbackID; if falseTarget == "" { falseTarget = flow.TerminalID }
			edges = append(edges, struct { id, label string; color color.Color }{trueTarget, "True", green}, struct { id, label string; color color.Color }{falseTarget, "False", red})
		}
		return edges
	}
	for len(queue) > 0 {
		id := queue[0]; queue = queue[1:]
		node, ok := nodes[id]; if !ok { continue }
		for _, edge := range outgoing(node) {
			if _, exists := nodes[edge.id]; !exists { continue }
			if _, seen := levels[edge.id]; seen { continue }
			level := levels[id]+1; levels[edge.id] = level; if level > maxLevel { maxLevel = level }; queue = append(queue, edge.id)
		}
	}
	for _, node := range flow.Nodes { if _, exists := levels[node.ID]; !exists { maxLevel++; levels[node.ID] = maxLevel } }
	layers := make(map[int][]graphNode)
	for _, node := range flow.Nodes { layers[levels[node.ID]] = append(layers[levels[node.ID]], node) }
	maxRows := 1
	for _, layer := range layers { if len(layer) > maxRows { maxRows = len(layer) } }
	diagramSize := fyne.NewSize(inset*2+float32(maxLevel+1)*nodeWidth+float32(maxLevel)*horizontalGap,
		inset*2+float32(maxRows)*nodeHeight+float32(maxRows-1)*verticalGap)
	contentSize := fyne.NewSize(max(diagramSize.Width, view.viewportSize().Width), max(diagramSize.Height, view.viewportSize().Height))
	centerX, centerY := (contentSize.Width-diagramSize.Width)/2, (contentSize.Height-diagramSize.Height)/2
	width, height := contentSize.Width, contentSize.Height
	background := canvas.NewRectangle(color.NRGBA{R: 246, G: 248, B: 251, A: 255})
	background.Resize(fyne.NewSize(width, height))
	objects := []fyne.CanvasObject{background}
	positions := make(map[string]fyne.Position, len(flow.Nodes))
	for level, layer := range layers {
		layerHeight := float32(len(layer))*nodeHeight + float32(max(len(layer)-1, 0))*verticalGap
		startY := (height-layerHeight)/2
		for row, node := range layer {
			positions[node.ID] = fyne.NewPos(centerX+inset+float32(level)*(nodeWidth+horizontalGap), centerY+startY+float32(row)*(nodeHeight+verticalGap))
		}
	}
	addLine := func(from, to fyne.Position, edgeColor color.Color) {
		line := canvas.NewLine(edgeColor); line.StrokeWidth = 2*zoom; line.Position1 = from; line.Position2 = to; objects = append(objects, line)
	}
	for _, node := range flow.Nodes {
		from, ok := positions[node.ID]; if !ok { continue }
		edges := outgoing(node)
		for edgeIndex, edge := range edges {
			to, targetOK := positions[edge.id]; if !targetOK { continue }
			edgeOffset := float32(0)
			if node.Type == "qualifier" && len(edges) > 1 { edgeOffset = (float32(edgeIndex)-float32(len(edges)-1)/2)*18*zoom }
			var start, end fyne.Position
			var labelPosition fyne.Position
			arrowFromVertical := false
			if edge.id == node.ID {
				start = fyne.NewPos(from.X+nodeWidth, from.Y+nodeHeight/2+edgeOffset)
				end = fyne.NewPos(from.X+nodeWidth/2+edgeOffset, from.Y)
				outerX, outerY := start.X+26*zoom, from.Y-18*zoom
				p1, p2, p3 := fyne.NewPos(outerX, start.Y), fyne.NewPos(outerX, outerY), fyne.NewPos(end.X, outerY)
				addLine(start,p1,edge.color); addLine(p1,p2,edge.color); addLine(p2,p3,edge.color); addLine(p3,end,edge.color)
				labelPosition = fyne.NewPos(outerX+3*zoom, outerY-12*zoom)
				arrowFromVertical = true
			} else if to.X > from.X {
				start = fyne.NewPos(from.X+nodeWidth, from.Y+nodeHeight/2+edgeOffset)
				end = fyne.NewPos(to.X, to.Y+nodeHeight/2+edgeOffset)
				midX := start.X+(end.X-start.X)/2
				p1, p2 := fyne.NewPos(midX,start.Y), fyne.NewPos(midX,end.Y)
				addLine(start,p1,edge.color); addLine(p1,p2,edge.color); addLine(p2,end,edge.color)
				labelPosition = fyne.NewPos(midX+4*zoom,(p1.Y+p2.Y)/2-12*zoom)
			} else {
				start = fyne.NewPos(from.X, from.Y+nodeHeight/2+edgeOffset)
				end = fyne.NewPos(to.X+nodeWidth/2, to.Y+nodeHeight)
				routeY := min(from.Y,to.Y)-24*zoom
				p1, p2 := fyne.NewPos(start.X-22*zoom,start.Y), fyne.NewPos(start.X-22*zoom,routeY)
				p3, p4 := fyne.NewPos(end.X,routeY), end
				addLine(start,p1,edge.color); addLine(p1,p2,edge.color); addLine(p2,p3,edge.color); addLine(p3,p4,edge.color)
				labelPosition = fyne.NewPos((p2.X+p3.X)/2,routeY-13*zoom)
				arrowFromVertical = true
			}
			if arrowFromVertical {
				addLine(end, fyne.NewPos(end.X-5*zoom, end.Y-5*zoom), edge.color)
				addLine(end, fyne.NewPos(end.X+5*zoom, end.Y-5*zoom), edge.color)
			} else if end.X >= start.X {
				addLine(end, fyne.NewPos(end.X-5*zoom, end.Y-5*zoom), edge.color)
				addLine(end, fyne.NewPos(end.X-5*zoom, end.Y+5*zoom), edge.color)
			} else {
				addLine(end, fyne.NewPos(end.X+5*zoom, end.Y-5*zoom), edge.color)
				addLine(end, fyne.NewPos(end.X+5*zoom, end.Y+5*zoom), edge.color)
			}
			label := canvas.NewText(edge.label, edge.color); label.TextSize = 11*zoom
			label.Move(labelPosition); objects = append(objects, label)
		}
	}
	for _, node := range flow.Nodes {
		position := positions[node.ID]
		name := strings.TrimSpace(node.Name); if name == "" { name = "Unnamed state" }
		textSize := float32(14)*zoom
		if runeCount := len([]rune(name)); runeCount > 19 {
			fitSize := (nodeWidth-18)/(float32(runeCount)*0.58)
			if fitSize < textSize { textSize = fitSize }
		}
		graphNode := newFlowGraphNodeWidget(name, node.ID == selectedID, textSize/14, func(id string) func() { return func() { selectNode(id) } }(node.ID))
		graphNode.Resize(fyne.NewSize(nodeWidth, nodeHeight)); graphNode.Move(position); objects = append(objects, graphNode)
	}
	view.background = background
	view.diagramSize = diagramSize
	view.content.Objects = objects
	view.content.Resize(contentSize)
	view.Refresh()
}

func addPlaceholderNames(text string, seen map[string]bool, names *[]string) {
	for _, match := range flowPlaceholderReference.FindAllStringSubmatch(text, -1) {
		if len(match) > 2 && !seen[match[2]] { seen[match[2]] = true; *names = append(*names, match[2]) }
	}
}

func executeFlowGraph(flow graphFlow, values map[string]string, run func(...string) (string, error), startServe func() error, logLine func(string)) bool {
	nodes := make(map[string]graphNode, len(flow.Nodes)); for _, node := range flow.Nodes { nodes[node.ID] = node }
	currentID := flow.StartID
	for visits := 0; visits < 1000; visits++ {
		node, exists := nodes[currentID]
		if !exists { logLine("[Terminal] transition points to missing state " + currentID); return false }
		logLine("[Flow] state " + graphNodeLabel(node))
		switch node.Type {
		case "start":
			if node.NextID == "" { logLine("[Terminal] Start has no next state"); return false }; logLine("[Transition] Start → " + graphExecutionTarget(nodes, node.NextID)); currentID = node.NextID
		case "success": logLine("[Flow] reached Success"); return true
		case "terminal": logLine("[Flow] reached Terminal"); return false
		case "command":
			args := make([]string, len(node.Args)); for i, arg := range node.Args { args[i] = resolveFlowValue(arg, values) }; args = append([]string{node.Command}, args...)
			logLine("[Request from Flow runner] -> " + formatCLICommand(args...))
			var output string; var err error
			if node.Command == "serve" { err = startServe(); if err == nil { output = "serve started"; waitAfterCLIAction() } } else { output, err = run(args...) }
			if err != nil { logLine("[Response from chrome-extension-cli] <- FAIL " + err.Error()); if strings.TrimSpace(output) != "" { logLine(strings.TrimSpace(output)) }; logLine("[Terminal] command failed"); return false }
			logLine("[Response from chrome-extension-cli] <- OK " + strings.TrimSpace(output))
			if node.NextID == "" { logLine("[Terminal] command has no next state"); return false }; logLine("[Transition] command success → " + graphExecutionTarget(nodes, node.NextID)); currentID = node.NextID
		case "qualifier":
			expression := resolveFlowValue(node.Expression, values)
			parsed, err := parseGraphExpression(expression); if err != nil { logLine("[Terminal] invalid qualifier: " + err.Error()); return false }
			matched := false
			for attempt := 0; attempt <= node.Retries; attempt++ {
				matched, err = evalGraphExpression(parsed, func(selector string) (bool, error) {
					logLine("[Request from Qualifier] -> " + formatCLICommand("page-contains", selector))
					out, callErr := run("page-contains", selector)
					if callErr != nil { logLine("[Response from page-contains] <- FAIL " + callErr.Error()); return false, callErr }
					switch strings.ToLower(strings.TrimSpace(out)) {
					case "true": logLine("[Response from page-contains] <- OK true"); return true, nil
					case "false": logLine("[Response from page-contains] <- OK false"); return false, nil
					default: return false, fmt.Errorf("page-contains returned %q", out)
					}
				})
				if err != nil { logLine("[Terminal] qualifier evaluation failed: " + err.Error()); return false }
				logLine(fmt.Sprintf("[Qualifier] result=%t attempt=%d/%d", matched, attempt+1, node.Retries+1))
				if matched { break }
				if attempt < node.Retries { logLine(fmt.Sprintf("[Qualifier] retrying; %d retries remain", node.Retries-attempt)) }
			}
			if node.Retries < 0 { logLine("[Terminal] qualifier retries cannot be negative"); return false }
			if matched { if node.SuccessID == "" { logLine("[Terminal] qualifier has no success target"); return false }; logLine("[Transition] qualifier true → " + graphExecutionTarget(nodes, node.SuccessID)); currentID = node.SuccessID
			} else if node.FallbackID == "" { logLine("[Terminal] qualifier false and no fallback target"); return false
			} else { logLine("[Transition] qualifier false after retries → " + graphExecutionTarget(nodes, node.FallbackID)); currentID = node.FallbackID }
		default: logLine("[Terminal] unknown state type " + node.Type); return false
		}
	}
	logLine("[Terminal] exceeded the 1000 state visit safety limit")
	return false
}

func graphExecutionTarget(nodes map[string]graphNode, id string) string {
	if node, ok := nodes[id]; ok { return graphNodeLabel(node) }
	return "missing state " + id + " (Terminal)"
}

func resolveFlowValue(text string, values map[string]string) string {
	return flowPlaceholderReference.ReplaceAllStringFunc(text, func(reference string) string {
		match := flowPlaceholderReference.FindStringSubmatch(reference); if len(match) < 3 { return reference }
		value, ok := values[match[2]]; if !ok { return reference }; return match[1] + value
	})
}

type graphExprNode struct { op string; value bool; selector string; left, right *graphExprNode }
type graphExprParser struct { tokens []string; at int }
var graphExprToken = regexp.MustCompile(`(?i)pageContains\s*\(\s*"(?:\\.|[^"\\])*"\s*\)|\bAND\b|\bOR\b|\bNOT\b|\bTRUE\b|\bFALSE\b|[()]`)

func parseGraphExpression(source string) (*graphExprNode, error) {
	var tokens []string; cursor := 0
	for _, span := range graphExprToken.FindAllStringIndex(source, -1) { if strings.TrimSpace(source[cursor:span[0]]) != "" { return nil, fmt.Errorf("unexpected text %q", strings.TrimSpace(source[cursor:span[0]])) }; tokens = append(tokens, strings.TrimSpace(source[span[0]:span[1]])); cursor = span[1] }
	if strings.TrimSpace(source[cursor:]) != "" { return nil, fmt.Errorf("unexpected text %q", strings.TrimSpace(source[cursor:])) }
	if len(tokens) == 0 { return nil, fmt.Errorf("expression is empty") }
	parser := &graphExprParser{tokens:tokens}; node, err := parser.parseOr(); if err != nil { return nil, err }; if parser.at != len(tokens) { return nil, fmt.Errorf("unexpected token %q", tokens[parser.at]) }; return node, nil
}
func (p *graphExprParser) parseOr() (*graphExprNode, error) { left, err := p.parseAnd(); if err != nil { return nil, err }; for p.at < len(p.tokens) && strings.EqualFold(p.tokens[p.at], "OR") { p.at++; right, e := p.parseAnd(); if e != nil { return nil, e }; left = &graphExprNode{op:"or", left:left, right:right} }; return left, nil }
func (p *graphExprParser) parseAnd() (*graphExprNode, error) { left, err := p.parseUnary(); if err != nil { return nil, err }; for p.at < len(p.tokens) && strings.EqualFold(p.tokens[p.at], "AND") { p.at++; right, e := p.parseUnary(); if e != nil { return nil, e }; left = &graphExprNode{op:"and", left:left, right:right} }; return left, nil }
func (p *graphExprParser) parseUnary() (*graphExprNode, error) { if p.at < len(p.tokens) && strings.EqualFold(p.tokens[p.at], "NOT") { p.at++; child, err := p.parseUnary(); if err != nil { return nil, err }; return &graphExprNode{op:"not", left:child}, nil }; return p.parsePrimary() }
func (p *graphExprParser) parsePrimary() (*graphExprNode, error) {
	if p.at >= len(p.tokens) { return nil, fmt.Errorf("expected a condition") }; token := p.tokens[p.at]; p.at++
	if token == "(" { node, err := p.parseOr(); if err != nil { return nil, err }; if p.at >= len(p.tokens) || p.tokens[p.at] != ")" { return nil, fmt.Errorf("missing closing parenthesis") }; p.at++; return node, nil }
	if token == ")" { return nil, fmt.Errorf("unexpected closing parenthesis") }
	if strings.EqualFold(token, "TRUE") { return &graphExprNode{op:"literal", value:true}, nil }
	if strings.EqualFold(token, "FALSE") { return &graphExprNode{op:"literal"}, nil }
	if strings.HasPrefix(strings.ToLower(token), "pagecontains") { start := strings.Index(token, `"`); end := strings.LastIndex(token, `"`); selector, err := strconv.Unquote(token[start:end+1]); if err != nil { return nil, err }; return &graphExprNode{op:"contains", selector:selector}, nil }
	return nil, fmt.Errorf("expected pageContains(\"selector\"), true, false, or parenthesized expression")
}
func evalGraphExpression(node *graphExprNode, contains func(string) (bool, error)) (bool, error) {
	switch node.op {
	case "literal": return node.value, nil
	case "contains": return contains(node.selector)
	case "not": value, err := evalGraphExpression(node.left, contains); return !value, err
	case "and": left, err := evalGraphExpression(node.left, contains); if err != nil || !left { return left, err }; return evalGraphExpression(node.right, contains)
	case "or": left, err := evalGraphExpression(node.left, contains); if err != nil || left { return left, err }; return evalGraphExpression(node.right, contains)
	default: return false, fmt.Errorf("unknown expression operation %q", node.op)
	}
}
