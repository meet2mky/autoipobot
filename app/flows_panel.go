package main

import (
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"

	"autoipo/internal/flow"
)

// flowsPanel is the Flows tab. It owns the saved flows (UI thread only) and
// switches between the list page, the builder and the run-logs page. The run
// drawer is docked under whichever page is visible.
type flowsPanel struct {
	ctx    *appContext
	flows  []flow.Flow
	pages  *fyne.Container
	list   *flowListPage
	editor *flowEditor
	runner *flowRunner
	drawer *runDrawer
	root   fyne.CanvasObject
}

func newFlowsPanel(ctx *appContext) *flowsPanel {
	p := &flowsPanel{ctx: ctx}
	p.loadFlows()
	p.drawer = newRunDrawer()
	p.runner = newFlowRunner(p)
	p.list = newFlowListPage(p)
	p.editor = newFlowEditor(p)
	p.pages = container.NewStack(p.list.root)
	root := container.NewBorder(nil, p.drawer.root, nil, nil, p.pages)
	p.drawer.onToggle = root.Refresh
	p.root = root
	return p
}

func (p *flowsPanel) loadFlows() {
	flows, warnings, err := p.ctx.flows.Load()
	if err != nil {
		p.ctx.addNotice(fmt.Errorf("load flows: %w", err))
	}
	for _, warning := range warnings {
		p.ctx.logEvent("flows: %s", warning)
	}
	if flows == nil {
		flows = []flow.Flow{}
	}
	p.flows = flows
}

func (p *flowsPanel) showPage(page fyne.CanvasObject) {
	p.pages.Objects = []fyne.CanvasObject{page}
	p.pages.Refresh()
}

// showList refreshes and displays the flow list.
func (p *flowsPanel) showList() {
	p.list.refresh()
	p.showPage(p.list.root)
}

// openEditor shows the builder for f (a copy is edited until Save).
func (p *flowsPanel) openEditor(f flow.Flow) {
	p.editor.open(f)
	p.showPage(p.editor.root)
}

func (p *flowsPanel) newFlow() {
	p.openEditor(flow.New(flow.UniqueName(p.flows, "New flow")))
}

func (p *flowsPanel) flowIndex(id string) int {
	for i := range p.flows {
		if p.flows[i].ID == id {
			return i
		}
	}
	return -1
}

// nameTaken reports whether another flow already uses name.
func (p *flowsPanel) nameTaken(name, exceptID string) bool {
	for _, f := range p.flows {
		if f.ID != exceptID && strings.EqualFold(strings.TrimSpace(f.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// upsertFlow saves f, replacing an existing flow with the same ID. On a
// failed write the in-memory list is left unchanged.
func (p *flowsPanel) upsertFlow(f flow.Flow) error {
	if err := f.Validate(); err != nil {
		return err
	}
	previous := p.flows
	updated := make([]flow.Flow, len(previous), len(previous)+1)
	copy(updated, previous)
	if index := p.flowIndex(f.ID); index >= 0 {
		updated[index] = f
	} else {
		updated = append(updated, f)
	}
	if err := p.ctx.flows.Save(updated); err != nil {
		return fmt.Errorf("save flows: %w", err)
	}
	p.flows = updated
	return nil
}

func (p *flowsPanel) deleteFlow(f flow.Flow) {
	dialog.ShowConfirm("Delete flow", fmt.Sprintf("Delete %q and its run logs?", f.Name), func(ok bool) {
		if !ok {
			return
		}
		index := p.flowIndex(f.ID)
		if index < 0 {
			return
		}
		updated := append(append([]flow.Flow(nil), p.flows[:index]...), p.flows[index+1:]...)
		if err := p.ctx.flows.Save(updated); err != nil {
			dialog.ShowError(fmt.Errorf("save flows: %w", err), p.ctx.window)
			return
		}
		p.flows = updated
		if err := flow.DeleteRuns(f.ID); err != nil {
			p.ctx.logEvent("delete run logs for %s: %v", f.Name, err)
		}
		p.ctx.logEvent("deleted flow %q", f.Name)
		p.list.refresh()
	}, p.ctx.window)
}

func (p *flowsPanel) duplicateFlow(f flow.Flow) {
	copied := f.Duplicate(flow.UniqueName(p.flows, "Copy of "+f.Name))
	if err := p.upsertFlow(copied); err != nil {
		dialog.ShowError(err, p.ctx.window)
		return
	}
	p.ctx.logEvent("duplicated flow %q as %q", f.Name, copied.Name)
	p.list.refresh()
}
