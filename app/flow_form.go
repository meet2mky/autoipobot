package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/internal/flow"
)

// terminalDefault is the target option meaning "no explicit target".
const terminalDefault = "(terminal by default)"

// Form row keys.
const (
	rowKind         = "kind"
	rowName         = "name"
	rowPlaceholders = "placeholders"
	rowCommand      = "command"
	rowArg0         = "arg0"
	rowArg1         = "arg1"
	rowSaveAs       = "saveAs"
	rowExpression   = "expression"
	rowRetries      = "retries"
	rowNext         = "next"
	rowTrue         = "true"
	rowFalse        = "false"
	rowActions      = "actions"
)

// nodeForm edits the selected node in place. Every control applies its
// change immediately to the editor's working copy.
type nodeForm struct {
	editor  *flowEditor
	loading bool

	title        *widget.Label
	hint         *widget.Label
	kind         *widget.Select
	name         *widget.Entry
	placeholders *growingEntry
	command      *widget.Select
	args         [2]*widget.Entry
	saveAs       *widget.Entry
	expression   *growingEntry
	retries      *widget.Entry
	next         *widget.Select
	onTrue       *widget.Select
	onFalse      *widget.Select
	targetIDs    map[string]string

	insertNext, insertTrue, insertFalse, deleteBtn *widget.Button

	rows map[string]*fyne.Container
	root *fyne.Container
}

func newNodeForm(e *flowEditor) *nodeForm {
	f := &nodeForm{editor: e, rows: map[string]*fyne.Container{}, targetIDs: map[string]string{}}
	f.title = boldLabel("Select a state on the canvas")
	f.hint = wrappedLabel("")
	f.buildInputs()
	f.buildTargets()
	f.buildActions()
	f.root = container.NewVBox(
		f.title, f.hint,
		f.row(rowKind, "State type", f.kind),
		f.row(rowName, "Name", f.name),
		f.row(rowPlaceholders, "Start values (@name=value per line)", f.placeholders),
		f.row(rowCommand, "Command", f.command),
		f.row(rowArg0, "Argument 1", f.args[0]),
		f.row(rowArg1, "Argument 2", f.args[1]),
		f.row(rowSaveAs, "Save output as @…", f.saveAs),
		f.row(rowExpression, "Expression", f.expression),
		f.row(rowRetries, "Retries", f.retries),
		f.row(rowNext, "Next state", f.next),
		f.row(rowTrue, "When true", f.onTrue),
		f.row(rowFalse, "When false (after retries)", f.onFalse),
		f.row(rowActions, "Actions", container.NewHBox(f.insertNext, f.insertTrue, f.insertFalse, f.deleteBtn)),
	)
	f.show(nil)
	return f
}

func (f *nodeForm) row(key, caption string, w fyne.CanvasObject) *fyne.Container {
	row := fieldRow(caption, w)
	f.rows[key] = row
	return row
}

func (f *nodeForm) buildInputs() {
	f.kind = widget.NewSelect([]string{flow.TypeCommand, flow.TypeQualifier}, func(kind string) {
		if f.loading {
			return
		}
		f.edit(func(node *flow.Node) {
			if node.Type != kind && !node.IsFixed() {
				convertNodeKind(node, kind)
			}
		})
		f.show(f.editor.selectedNode())
	})
	f.name = hintEntry("State name")
	f.name.OnChanged = func(value string) { f.edit(func(node *flow.Node) { node.Name = value }) }
	f.placeholders = newGrowingEntry("@username=myusername\n@account=12345")
	f.placeholders.OnChanged = func(value string) {
		f.placeholders.fit(value)
		f.edit(func(node *flow.Node) { node.PlaceholderText = value })
	}
	f.command = widget.NewSelect(flow.Commands, func(command string) {
		if f.loading {
			return
		}
		f.edit(func(node *flow.Node) {
			node.Command = command
			node.Args = resizeArgs(node.Args, flow.CommandArgCount(command))
		})
		f.showArgs(f.editor.selectedNode())
		f.relayout()
	})
	for i := range f.args {
		index := i
		f.args[i] = hintEntry("")
		f.args[i].OnChanged = func(value string) {
			f.edit(func(node *flow.Node) {
				if index < len(node.Args) {
					node.Args[index] = value
				}
			})
		}
	}
	f.saveAs = hintEntry("name (optional) — later steps can use @name")
	f.saveAs.OnChanged = func(value string) {
		f.edit(func(node *flow.Node) { node.SaveAs = strings.TrimPrefix(strings.TrimSpace(value), "@") })
	}
	f.expression = newGrowingEntry(`pageContains("#kc-login") AND NOT pageContains(".error")`)
	f.expression.OnChanged = func(value string) {
		f.expression.fit(value)
		f.edit(func(node *flow.Node) { node.Expression = value })
	}
	f.retries = hintEntry("0")
	f.retries.OnChanged = func(value string) {
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed >= 0 {
			f.edit(func(node *flow.Node) { node.Retries = parsed })
		}
	}
}

func (f *nodeForm) buildTargets() {
	f.next = widget.NewSelect([]string{terminalDefault}, func(label string) {
		f.edit(func(node *flow.Node) { node.NextID = f.targetIDs[label] })
	})
	f.onTrue = widget.NewSelect([]string{terminalDefault}, func(label string) {
		f.edit(func(node *flow.Node) { node.SuccessID = f.targetIDs[label] })
	})
	f.onFalse = widget.NewSelect([]string{terminalDefault}, func(label string) {
		f.edit(func(node *flow.Node) { node.FallbackID = f.targetIDs[label] })
	})
}

func (f *nodeForm) buildActions() {
	f.insertNext = widget.NewButton("Insert step after", func() { f.editor.insertAfter("next") })
	f.insertTrue = widget.NewButton("Insert on true", func() { f.editor.insertAfter("true") })
	f.insertFalse = widget.NewButton("Insert on false", func() { f.editor.insertAfter("false") })
	f.deleteBtn = widget.NewButton("Delete state", f.editor.deleteSelected)
	f.deleteBtn.Importance = widget.DangerImportance
}

// edit applies change to the selected node unless the form is being
// populated, then tells the editor to redraw.
func (f *nodeForm) edit(change func(node *flow.Node)) {
	if f.loading {
		return
	}
	node := f.editor.selectedNode()
	if node == nil {
		return
	}
	change(node)
	f.editor.nodeChanged()
}

// show populates the form for node (nil clears it).
func (f *nodeForm) show(node *flow.Node) {
	f.loading = true
	defer func() { f.loading = false }()
	defer f.relayout()
	for _, row := range f.rows {
		row.Hide()
	}
	for _, button := range []*widget.Button{f.insertNext, f.insertTrue, f.insertFalse, f.deleteBtn} {
		button.Hide()
	}
	if node == nil {
		f.title.SetText("Select a state on the canvas")
		f.hint.SetText("")
		return
	}
	f.title.SetText(strings.ToUpper(node.Type) + "  [" + node.ShortID() + "]")
	f.hint.SetText(nodeHint(node.Type))
	switch node.Type {
	case flow.TypeStart:
		f.placeholders.setText(node.PlaceholderText)
		f.showRows(rowPlaceholders, rowNext, rowActions)
		f.insertNext.Show()
	case flow.TypeCommand:
		f.kind.SetSelected(flow.TypeCommand)
		f.name.SetText(node.Name)
		f.command.ClearSelected()
		f.command.SetSelected(node.Command)
		f.saveAs.SetText(node.SaveAs)
		f.showRows(rowKind, rowName, rowCommand, rowSaveAs, rowNext, rowActions)
		f.showArgs(node)
		f.insertNext.Show()
		f.deleteBtn.Show()
	case flow.TypeQualifier:
		f.kind.SetSelected(flow.TypeQualifier)
		f.name.SetText(node.Name)
		f.expression.setText(node.Expression)
		f.retries.SetText(strconv.Itoa(node.Retries))
		f.showRows(rowKind, rowName, rowExpression, rowRetries, rowTrue, rowFalse, rowActions)
		f.insertTrue.Show()
		f.insertFalse.Show()
		f.deleteBtn.Show()
	}
	f.refreshTargets()
}

// relayout re-runs the form's layout after rows or buttons were shown or
// hidden. Fyne's Show/Hide only flip a flag, and a container whose size did
// not change is not laid out again, so without this a row that was hidden and
// shown again could keep the position it had in a different row set.
func (f *nodeForm) relayout() {
	f.root.Refresh()
}

func nodeHint(kind string) string {
	switch kind {
	case flow.TypeStart:
		return "Every run begins here. Start values are the defaults offered in the Play dialog."
	case flow.TypeCommand:
		return "Runs one bridge command. Arguments may reference @placeholders."
	case flow.TypeQualifier:
		return "Evaluates a page condition and branches. Retries re-check the condition after a pause."
	case flow.TypeSuccess:
		return "Reaching this state ends the run successfully."
	default:
		return "Reaching this state ends the run as failed; a screenshot is captured."
	}
}

func (f *nodeForm) showRows(keys ...string) {
	for _, key := range keys {
		f.rows[key].Show()
	}
}

// showArgs shows the argument rows the selected command needs, with hints.
func (f *nodeForm) showArgs(node *flow.Node) {
	if node == nil || node.Type != flow.TypeCommand {
		return
	}
	wasLoading := f.loading
	f.loading = true
	defer func() { f.loading = wasLoading }()
	count := flow.CommandArgCount(node.Command)
	node.Args = resizeArgs(node.Args, count)
	hints := flow.ArgPlaceholders(node.Command)
	for i, entry := range f.args {
		key := rowArg0
		if i == 1 {
			key = rowArg1
		}
		if i >= count {
			f.rows[key].Hide()
			continue
		}
		hint := fmt.Sprintf("Argument %d", i+1)
		if i < len(hints) {
			hint = hints[i]
		}
		entry.SetPlaceHolder(hint)
		entry.SetText(node.Args[i])
		f.rows[key].Show()
	}
}

func resizeArgs(args []string, count int) []string {
	resized := make([]string, count)
	copy(resized, args)
	return resized
}

// refreshTargets rebuilds the target options from the current nodes and
// re-selects the node's transitions without triggering edits.
func (f *nodeForm) refreshTargets() {
	wasLoading := f.loading
	f.loading = true
	defer func() { f.loading = wasLoading }()
	options := []string{terminalDefault}
	f.targetIDs = map[string]string{terminalDefault: ""}
	for _, node := range f.editor.flow.Nodes {
		label := node.Label()
		options = append(options, label)
		f.targetIDs[label] = node.ID
	}
	node := f.editor.selectedNode()
	if node == nil {
		return
	}
	for _, target := range []struct {
		sel *widget.Select
		id  string
	}{{f.next, node.NextID}, {f.onTrue, node.SuccessID}, {f.onFalse, node.FallbackID}} {
		target.sel.SetOptions(options)
		target.sel.SetSelected(f.editor.flow.TargetLabel(target.id))
	}
}

// validate reports problems in the visible controls that cannot be applied
// live (currently the retries entry).
func (f *nodeForm) validate() error {
	node := f.editor.selectedNode()
	if node == nil || node.Type != flow.TypeQualifier {
		return nil
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(f.retries.Text)); err != nil || parsed < 0 {
		return errors.New("qualifier retries must be a whole number of 0 or greater")
	}
	return nil
}

// convertNodeKind switches a node between command and qualifier, keeping
// its outgoing transition where possible.
func convertNodeKind(node *flow.Node, kind string) {
	if kind == flow.TypeQualifier {
		node.Type = flow.TypeQualifier
		node.SuccessID, node.NextID = node.NextID, ""
		node.Command, node.Args, node.SaveAs = "", nil, ""
		node.Expression, node.Retries, node.FallbackID = "", 0, ""
		return
	}
	node.Type = flow.TypeCommand
	node.NextID = node.SuccessID
	if node.NextID == "" {
		node.NextID = node.FallbackID
	}
	node.Command, node.Args = "open", nil
	node.Expression, node.Retries, node.SuccessID, node.FallbackID = "", 0, "", ""
}
