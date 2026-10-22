// Package graphlayout positions flow states for the builder canvas and routes
// the edges between them. It has no UI dependencies so the geometry can be
// unit-tested natively.
package graphlayout

// Zoom bounds shared by the canvas controls.
const (
	MinZoom float32 = 0.15
	MaxZoom float32 = 2.5
)

// Point is a position in canvas coordinates.
type Point struct{ X, Y float32 }

// Size is a width/height pair in canvas coordinates.
type Size struct{ Width, Height float32 }

// Options describe the unscaled geometry of the diagram.
type Options struct {
	NodeWidth, NodeHeight       float32
	HorizontalGap, VerticalGap  float32
	Margin                      float32
	Zoom                        float32
	EdgeSpread, LoopReach       float32 // qualifier edge separation; self-loop extent
	BackRouteGap, BackRouteLift float32 // backward edge detour sizes
}

// DefaultOptions mirror the sizes used by the original canvas at 100% zoom.
func DefaultOptions() Options {
	return Options{
		NodeWidth: 164, NodeHeight: 56,
		HorizontalGap: 148, VerticalGap: 48,
		Margin: 36, Zoom: 1,
		EdgeSpread: 18, LoopReach: 26,
		BackRouteGap: 22, BackRouteLift: 24,
	}
}

// ClampZoom keeps zoom within [MinZoom, MaxZoom].
func ClampZoom(zoom float32) float32 {
	if zoom < MinZoom {
		return MinZoom
	}
	if zoom > MaxZoom {
		return MaxZoom
	}
	return zoom
}

func (o Options) scaled() Options {
	zoom := ClampZoom(o.Zoom)
	return Options{
		NodeWidth: o.NodeWidth * zoom, NodeHeight: o.NodeHeight * zoom,
		HorizontalGap: o.HorizontalGap * zoom, VerticalGap: o.VerticalGap * zoom,
		Margin: o.Margin * zoom, Zoom: zoom,
		EdgeSpread: o.EdgeSpread * zoom, LoopReach: o.LoopReach * zoom,
		BackRouteGap: o.BackRouteGap * zoom, BackRouteLift: o.BackRouteLift * zoom,
	}
}

// Layout is the result of Compute.
type Layout struct {
	// Positions holds the top-left corner of every node.
	Positions map[string]Point
	// Size is the diagram's bounding box including margins.
	Size Size
	// NodeSize is the scaled size of one node.
	NodeSize Size
	// Scaled holds the scaled options used for edge routing.
	Scaled Options
}

// Compute lays out layers left to right. Nodes within a layer are stacked
// top to bottom and each layer is centred vertically on the tallest layer.
func Compute(layers [][]string, opts Options) Layout {
	scaled := opts.scaled()
	layout := Layout{Positions: map[string]Point{}, NodeSize: Size{scaled.NodeWidth, scaled.NodeHeight}, Scaled: scaled}
	columns := len(layers)
	rows := 0
	for _, layer := range layers {
		if len(layer) > rows {
			rows = len(layer)
		}
	}
	if columns == 0 || rows == 0 {
		layout.Size = Size{scaled.Margin * 2, scaled.Margin * 2}
		return layout
	}
	layout.Size = Size{
		Width:  scaled.Margin*2 + float32(columns)*scaled.NodeWidth + float32(columns-1)*scaled.HorizontalGap,
		Height: scaled.Margin*2 + float32(rows)*scaled.NodeHeight + float32(rows-1)*scaled.VerticalGap,
	}
	for column, layer := range layers {
		layerHeight := float32(len(layer))*scaled.NodeHeight + float32(max(len(layer)-1, 0))*scaled.VerticalGap
		top := (layout.Size.Height - layerHeight) / 2
		x := scaled.Margin + float32(column)*(scaled.NodeWidth+scaled.HorizontalGap)
		for row, id := range layer {
			layout.Positions[id] = Point{X: x, Y: top + float32(row)*(scaled.NodeHeight+scaled.VerticalGap)}
		}
	}
	return layout
}

// FitZoom returns the largest zoom, capped at 100%, at which the diagram fits
// inside viewport.
func FitZoom(layers [][]string, viewport Size, opts Options) float32 {
	opts.Zoom = 1
	natural := Compute(layers, opts).Size
	if natural.Width <= 0 || natural.Height <= 0 || viewport.Width <= 0 || viewport.Height <= 0 {
		return 1
	}
	zoom := min(viewport.Width/natural.Width, viewport.Height/natural.Height)
	if zoom > 1 {
		zoom = 1
	}
	return ClampZoom(zoom)
}

// Arrow is the direction in which an edge enters its target.
type Arrow int

// Arrow directions.
const (
	ArrowRight Arrow = iota
	ArrowDown
)

// Path is a routed edge: an orthogonal polyline, a label anchor and the
// arrowhead direction at the final point.
type Path struct {
	Points []Point
	Label  Point
	Arrow  Arrow
}

// Route returns the polyline for an edge between two node corners. offset
// shifts the attachment points vertically so parallel edges do not overlap.
func Route(from, to Point, layout Layout, offset float32) Path {
	o := layout.Scaled
	w, h := layout.NodeSize.Width, layout.NodeSize.Height
	switch {
	case from == to:
		start := Point{from.X + w, from.Y + h/2 + offset}
		end := Point{from.X + w/2, from.Y}
		outerX, outerY := start.X+o.LoopReach, from.Y-o.BackRouteLift*0.75
		return Path{
			Points: []Point{start, {outerX, start.Y}, {outerX, outerY}, {end.X, outerY}, end},
			Label:  Point{outerX + 3*o.Zoom, outerY - 12*o.Zoom},
			Arrow:  ArrowDown,
		}
	case to.X > from.X:
		start := Point{from.X + w, from.Y + h/2 + offset}
		end := Point{to.X, to.Y + h/2 + offset}
		midX := start.X + (end.X-start.X)/2
		return Path{
			Points: []Point{start, {midX, start.Y}, {midX, end.Y}, end},
			Label:  Point{midX + 4*o.Zoom, (start.Y+end.Y)/2 - 12*o.Zoom},
			Arrow:  ArrowRight,
		}
	default:
		start := Point{from.X, from.Y + h/2 + offset}
		end := Point{to.X + w/2, to.Y}
		detourX := start.X - o.BackRouteGap
		routeY := min(from.Y, to.Y) - o.BackRouteLift
		return Path{
			Points: []Point{start, {detourX, start.Y}, {detourX, routeY}, {end.X, routeY}, end},
			Label:  Point{(detourX + end.X) / 2, routeY - 13*o.Zoom},
			Arrow:  ArrowDown,
		}
	}
}

// ArrowHead returns the two short strokes forming the arrowhead at tip.
func ArrowHead(tip Point, arrow Arrow, zoom float32) [2][2]Point {
	size := 5 * zoom
	if arrow == ArrowDown {
		return [2][2]Point{
			{tip, {tip.X - size, tip.Y - size}},
			{tip, {tip.X + size, tip.Y - size}},
		}
	}
	return [2][2]Point{
		{tip, {tip.X - size, tip.Y - size}},
		{tip, {tip.X - size, tip.Y + size}},
	}
}
