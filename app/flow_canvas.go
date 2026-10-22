package main

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"autoipo/app/internal/graphlayout"
	"autoipo/internal/flow"
)

// Canvas palette (the diagram is drawn whiteboard-style in every theme).
var (
	canvasBackground = color.NRGBA{R: 246, G: 248, B: 251, A: 255}
	nodeFill         = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	nodeFillRunning  = color.NRGBA{R: 220, G: 252, B: 231, A: 255}
	nodeText         = color.NRGBA{R: 39, G: 49, B: 62, A: 255}
	strokeDefault    = color.NRGBA{R: 178, G: 190, B: 205, A: 255}
	strokeLint       = color.NRGBA{R: 245, G: 158, B: 11, A: 255}
	strokeRunning    = color.NRGBA{R: 34, G: 197, B: 94, A: 255}
	edgeNext         = color.NRGBA{R: 75, G: 117, B: 169, A: 255}
	edgeTrue         = color.NRGBA{R: 44, G: 145, B: 93, A: 255}
	edgeFalse        = color.NRGBA{R: 193, G: 82, B: 82, A: 255}
)

// canvasView is everything the canvas needs to draw one frame.
type canvasView struct {
	flow       flow.Flow
	selectedID string
	runningID  string
	issues     map[string]bool
	zoom       float32
}

// flowCanvas draws the flow graph: rectangles for states, orthogonal lines
// for transitions. The diagram is centred when the viewport is larger than
// the drawing.
type flowCanvas struct {
	widget.BaseWidget
	background  *canvas.Rectangle
	diagram     *fyne.Container
	diagramSize fyne.Size
	onSelect    func(id string)
}

func newFlowCanvas(onSelect func(id string)) *flowCanvas {
	c := &flowCanvas{
		background: canvas.NewRectangle(canvasBackground),
		diagram:    container.NewWithoutLayout(),
		onSelect:   onSelect,
	}
	c.ExtendBaseWidget(c)
	return c
}

// CreateRenderer implements fyne.Widget.
func (c *flowCanvas) CreateRenderer() fyne.WidgetRenderer {
	return &flowCanvasRenderer{canvas: c}
}

// render rebuilds the drawing for view.
func (c *flowCanvas) render(view canvasView) {
	layers := idLayers(view.flow.Layers())
	opts := graphlayout.DefaultOptions()
	opts.Zoom = view.zoom
	layout := graphlayout.Compute(layers, opts)

	objects := c.edgeObjects(view, layout)
	objects = append(objects, c.nodeObjects(view, layout)...)
	c.diagramSize = fyne.NewSize(layout.Size.Width, layout.Size.Height)
	c.diagram.Objects = objects
	c.diagram.Resize(c.diagramSize)
	c.Refresh()
}

func idLayers(layers [][]flow.Node) [][]string {
	ids := make([][]string, len(layers))
	for i, layer := range layers {
		for _, node := range layer {
			ids[i] = append(ids[i], node.ID)
		}
	}
	return ids
}

func (c *flowCanvas) edgeObjects(view canvasView, layout graphlayout.Layout) []fyne.CanvasObject {
	var objects []fyne.CanvasObject
	zoom := layout.Scaled.Zoom
	for _, node := range view.flow.Nodes {
		from, ok := layout.Positions[node.ID]
		if !ok {
			continue
		}
		edges := view.flow.Edges(node)
		for index, edge := range edges {
			to, ok := layout.Positions[edge.To]
			if !ok {
				continue
			}
			offset := float32(0)
			if len(edges) > 1 {
				offset = (float32(index) - float32(len(edges)-1)/2) * layout.Scaled.EdgeSpread
			}
			path := graphlayout.Route(from, to, layout, offset)
			stroke := edgeColor(edge)
			for i := 1; i < len(path.Points); i++ {
				objects = append(objects, lineBetween(path.Points[i-1], path.Points[i], stroke, zoom))
			}
			for _, head := range graphlayout.ArrowHead(path.Points[len(path.Points)-1], path.Arrow, zoom) {
				objects = append(objects, lineBetween(head[0], head[1], stroke, zoom))
			}
			label := canvas.NewText(edge.Label, stroke)
			label.TextSize = 11 * zoom
			label.Move(fyne.NewPos(path.Label.X, path.Label.Y))
			label.Resize(label.MinSize())
			objects = append(objects, label)
		}
	}
	return objects
}

func edgeColor(edge flow.Edge) color.Color {
	switch {
	case edge.Default, edge.Label == "False":
		return edgeFalse
	case edge.Label == "True":
		return edgeTrue
	default:
		return edgeNext
	}
}

func lineBetween(a, b graphlayout.Point, stroke color.Color, zoom float32) *canvas.Line {
	line := canvas.NewLine(stroke)
	line.StrokeWidth = 2 * zoom
	line.Position1 = fyne.NewPos(a.X, a.Y)
	line.Position2 = fyne.NewPos(b.X, b.Y)
	return line
}

func (c *flowCanvas) nodeObjects(view canvasView, layout graphlayout.Layout) []fyne.CanvasObject {
	objects := make([]fyne.CanvasObject, 0, len(view.flow.Nodes))
	size := fyne.NewSize(layout.NodeSize.Width, layout.NodeSize.Height)
	for _, node := range view.flow.Nodes {
		position, ok := layout.Positions[node.ID]
		if !ok {
			continue
		}
		style := nodeStyleFor(node.ID, view)
		id := node.ID
		item := newFlowNode(nodeTitle(node), style, layout.Scaled.Zoom, func() {
			if c.onSelect != nil {
				c.onSelect(id)
			}
		})
		item.Resize(size)
		item.Move(fyne.NewPos(position.X, position.Y))
		objects = append(objects, item)
	}
	return objects
}

func nodeTitle(node flow.Node) string {
	name := strings.TrimSpace(node.Name)
	if name == "" {
		name = "Unnamed state"
	}
	if node.Type == flow.TypeCommand && node.Command != "" {
		name += " · " + node.Command
	}
	return name
}

// nodeStyle is the visual state of one rectangle.
type nodeStyle struct {
	fill        color.Color
	stroke      color.Color
	strokeWidth float32
}

func nodeStyleFor(id string, view canvasView) nodeStyle {
	style := nodeStyle{fill: nodeFill, stroke: strokeDefault, strokeWidth: 1}
	if view.issues[id] {
		style.stroke, style.strokeWidth = strokeLint, 2
	}
	if id == view.selectedID {
		style.stroke, style.strokeWidth = accentPrimary, 3
	}
	if id == view.runningID {
		style.fill, style.stroke, style.strokeWidth = nodeFillRunning, strokeRunning, 3
	}
	return style
}

type flowCanvasRenderer struct {
	canvas *flowCanvas
}

// Layout stretches the background over the viewport and centres the diagram.
func (r *flowCanvasRenderer) Layout(size fyne.Size) {
	r.canvas.background.Resize(size)
	diagram := r.canvas.diagramSize
	x := max((size.Width-diagram.Width)/2, 0)
	y := max((size.Height-diagram.Height)/2, 0)
	r.canvas.diagram.Move(fyne.NewPos(x, y))
	r.canvas.diagram.Resize(diagram)
}

func (r *flowCanvasRenderer) MinSize() fyne.Size { return r.canvas.diagramSize }

func (r *flowCanvasRenderer) Refresh() {
	r.Layout(r.canvas.Size())
	r.canvas.background.Refresh()
	r.canvas.diagram.Refresh()
}

func (r *flowCanvasRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.canvas.background, r.canvas.diagram}
}

func (r *flowCanvasRenderer) Destroy() {}

// flowNode is one tappable state rectangle.
type flowNode struct {
	widget.BaseWidget
	title    string
	style    nodeStyle
	textSize float32
	onTap    func()
}

func newFlowNode(title string, style nodeStyle, zoom float32, onTap func()) *flowNode {
	textSize := 14 * zoom
	if runes := len([]rune(title)); runes > 19 {
		if fit := (164*zoom - 18) / (float32(runes) * 0.58); fit < textSize {
			textSize = fit
		}
	}
	n := &flowNode{title: title, style: style, textSize: textSize, onTap: onTap}
	n.ExtendBaseWidget(n)
	return n
}

// Tapped implements fyne.Tappable.
func (n *flowNode) Tapped(*fyne.PointEvent) {
	if n.onTap != nil {
		n.onTap()
	}
}

// CreateRenderer implements fyne.Widget.
func (n *flowNode) CreateRenderer() fyne.WidgetRenderer {
	background := canvas.NewRectangle(n.style.fill)
	background.StrokeColor = n.style.stroke
	background.StrokeWidth = n.style.strokeWidth
	background.CornerRadius = 6
	label := canvas.NewText(n.title, nodeText)
	label.Alignment = fyne.TextAlignCenter
	label.TextStyle = fyne.TextStyle{Bold: true}
	label.TextSize = n.textSize
	return &flowNodeRenderer{background: background, label: label}
}

type flowNodeRenderer struct {
	background *canvas.Rectangle
	label      *canvas.Text
}

func (r *flowNodeRenderer) Layout(size fyne.Size) {
	r.background.Resize(size)
	r.label.Resize(fyne.NewSize(size.Width-18, size.Height-10))
	r.label.Move(fyne.NewPos(9, 5))
}

func (r *flowNodeRenderer) MinSize() fyne.Size { return fyne.NewSize(40, 20) }

func (r *flowNodeRenderer) Refresh() {
	r.background.Refresh()
	r.label.Refresh()
}

func (r *flowNodeRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.background, r.label}
}

func (r *flowNodeRenderer) Destroy() {}
