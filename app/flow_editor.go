package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/graphlayout"
	"autoipo/internal/flow"
)

// maxLintRows caps the lint list shown under the canvas.
const maxLintRows = 8

// flowEditor is the builder page: canvas on the left, node form on the
// right, lint issues and zoom controls under the canvas. It edits a working
// copy that is persisted only by Save.
type flowEditor struct {
	panel      *flowsPanel
	flow       flow.Flow
	selectedID string
	runningID  string
	zoom       float32
	dirty      bool
	loading    bool

	canvas       *flowCanvas
	canvasScroll *container.Scroll
	form         *nodeForm
	lintRows     *fyne.Container
	nameEntry    *widget.Entry
	keepEntry    *widget.Entry
	zoomLabel    *widget.Label
	root         fyne.CanvasObject
}

func newFlowEditor(p *flowsPanel) *flowEditor {
	e := &flowEditor{panel: p, zoom: 1, flow: flow.New("")}
	e.canvas = newFlowCanvas(e.selectNode)
	e.canvasScroll = container.NewScroll(e.canvas)
	e.form = newNodeForm(e)
	e.lintRows = container.NewVBox()
	e.nameEntry = hintEntry("Flow name")
	e.nameEntry.OnChanged = func(value string) {
		if !e.loading {
			e.flow.Name = value
			e.dirty = true
		}
	}
	e.keepEntry = hintEntry("10")
	e.keepEntry.OnChanged = func(string) {
		if !e.loading {
			e.dirty = true
		}
	}
	e.zoomLabel = widget.NewLabel("100%")

	save := widget.NewButton("Save", e.save)
	save.Importance = widget.HighImportance
	top := container.NewBorder(nil, nil,
		widget.NewButton("Back", e.back),
		container.NewHBox(widget.NewLabel("Keep run logs"), e.keepEntry, widget.NewButton("Add state", e.addNode), save),
		e.nameEntry)
	zoomBar := container.NewHBox(
		widget.NewButton("−", func() { e.setZoom(e.zoom * 0.8) }),
		e.zoomLabel,
		widget.NewButton("+", func() { e.setZoom(e.zoom * 1.25) }),
		widget.NewButton("Fit", e.fitZoom),
		widget.NewButton("100%", func() { e.setZoom(1) }),
	)
	lint := widget.NewCard("Lint", "", e.lintRows)
	left := container.NewBorder(nil, container.NewVBox(zoomBar, lint), nil, nil, e.canvasScroll)
	split := container.NewHSplit(left, container.NewVScroll(e.form.root))
	split.SetOffset(0.64)
	e.root = container.NewBorder(top, nil, nil, nil, split)
	return e
}

// open loads a copy of f into the editor.
func (e *flowEditor) open(f flow.Flow) {
	e.flow = f.Clone()
	e.dirty = false
	e.loading = true
	e.nameEntry.SetText(e.flow.Name)
	e.keepEntry.SetText(strconv.Itoa(max(e.flow.RunsToKeep, 1)))
	e.loading = false
	e.selectedID = e.flow.StartID
	e.form.show(e.selectedNode())
	e.fitZoom()
}

func (e *flowEditor) viewport() graphlayout.Size {
	size := e.canvasScroll.Size()
	if size.Width < 100 || size.Height < 100 {
		return graphlayout.Size{Width: 720, Height: 480}
	}
	return graphlayout.Size{Width: size.Width, Height: size.Height}
}

func (e *flowEditor) fitZoom() {
	e.setZoom(graphlayout.FitZoom(idLayers(e.flow.Layers()), e.viewport(), graphlayout.DefaultOptions()))
}

func (e *flowEditor) setZoom(zoom float32) {
	e.zoom = graphlayout.ClampZoom(zoom)
	e.zoomLabel.SetText(fmt.Sprintf("%.0f%%", e.zoom*100))
	e.refresh()
}

// refresh redraws the canvas, the lint list and the form's target options.
func (e *flowEditor) refresh() {
	issues := e.flow.Lint()
	flagged := make(map[string]bool, len(issues))
	for _, issue := range issues {
		flagged[issue.NodeID] = true
	}
	e.canvas.render(canvasView{flow: e.flow, selectedID: e.selectedID, runningID: e.runningID, issues: flagged, zoom: e.zoom})
	e.canvasScroll.Refresh() // adopt the new diagram size
	e.renderLint(issues)
	e.form.refreshTargets()
}

func (e *flowEditor) renderLint(issues []flow.Issue) {
	e.lintRows.Objects = nil
	if len(issues) == 0 {
		e.lintRows.Add(widget.NewLabel("No issues."))
	}
	for i, issue := range issues {
		if i == maxLintRows {
			e.lintRows.Add(widget.NewLabel(fmt.Sprintf("… and %d more", len(issues)-maxLintRows)))
			break
		}
		title := issue.NodeID
		if node, ok := e.flow.Node(issue.NodeID); ok {
			title = nodeTitle(node)
		}
		id := issue.NodeID
		row := widget.NewButton("⚠ "+title+": "+issue.Message, func() { e.selectNode(id) })
		row.Alignment = widget.ButtonAlignLeading
		row.Importance = widget.LowImportance
		e.lintRows.Add(row)
	}
	e.lintRows.Refresh()
}

func (e *flowEditor) selectedNode() *flow.Node {
	if index := e.flow.NodeIndex(e.selectedID); index >= 0 {
		return &e.flow.Nodes[index]
	}
	return nil
}

func (e *flowEditor) selectNode(id string) {
	e.selectedID = id
	e.form.show(e.selectedNode())
	e.refresh()
}

// nodeChanged is called by the form after it mutated the selected node.
func (e *flowEditor) nodeChanged() {
	e.dirty = true
	e.refresh()
}

func (e *flowEditor) addNode() {
	node := flow.NewCommandNode()
	e.flow.Nodes = append(e.flow.Nodes, node)
	e.dirty = true
	e.selectNode(node.ID)
}

// insertAfter adds a command node on the selected node's route
// ("next", "true" or "false").
func (e *flowEditor) insertAfter(route string) {
	node := flow.NewCommandNode()
	if !e.flow.InsertAfter(e.selectedID, route, node) {
		return
	}
	e.dirty = true
	e.selectNode(node.ID)
}

func (e *flowEditor) deleteSelected() {
	node := e.selectedNode()
	if node == nil || node.IsFixed() {
		return
	}
	id := node.ID
	dialog.ShowConfirm("Delete state", fmt.Sprintf("Delete %q? Transitions into it will route to Terminal by default.", nodeTitle(*node)), func(ok bool) {
		if ok && e.flow.RemoveNode(id) {
			e.dirty = true
			e.selectNode(e.flow.StartID)
		}
	}, e.panel.ctx.window)
}

func (e *flowEditor) back() {
	if !e.dirty {
		e.panel.showList()
		return
	}
	dialog.ShowConfirm("Discard changes?", "The flow has unsaved changes. Leave without saving?", func(ok bool) {
		if ok {
			e.dirty = false
			e.panel.showList()
		}
	}, e.panel.ctx.window)
}

func (e *flowEditor) save() {
	if err := e.validate(); err != nil {
		dialog.ShowError(err, e.panel.ctx.window)
		return
	}
	e.flow.UpdatedAt = time.Now()
	if err := e.panel.upsertFlow(e.flow); err != nil {
		dialog.ShowError(err, e.panel.ctx.window)
		return
	}
	e.dirty = false
	e.panel.ctx.logEvent("saved flow %q", e.flow.Name)
	e.panel.showList()
}

// validate normalises the working copy and reports the first problem that
// prevents saving. Lint issues do not block saving.
func (e *flowEditor) validate() error {
	name := strings.TrimSpace(e.nameEntry.Text)
	if name == "" {
		return errors.New("enter a flow name")
	}
	if e.panel.nameTaken(name, e.flow.ID) {
		return fmt.Errorf("another flow is already named %q", name)
	}
	e.flow.Name = name
	keep, err := strconv.Atoi(strings.TrimSpace(e.keepEntry.Text))
	if err != nil || keep < 1 {
		return errors.New("run log retention must be a whole number of at least 1")
	}
	e.flow.RunsToKeep = keep
	if start := e.flow.StartNode(); start != nil {
		values, err := flow.ParsePlaceholders(start.PlaceholderText)
		if err != nil {
			return fmt.Errorf("start values: %w", err)
		}
		start.Placeholders = values
	}
	if err := e.form.validate(); err != nil {
		return err
	}
	return e.flow.Validate()
}

// setRunningNode highlights the state a run is executing (empty clears it).
func (e *flowEditor) setRunningNode(flowID, nodeID string) {
	if e.flow.ID != flowID {
		nodeID = ""
	}
	if e.runningID == nodeID {
		return
	}
	e.runningID = nodeID
	e.refresh()
}
