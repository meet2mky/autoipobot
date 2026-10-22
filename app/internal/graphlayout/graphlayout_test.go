package graphlayout

import (
	"math"
	"testing"
)

func approx(a, b float32) bool { return math.Abs(float64(a-b)) < 0.01 }

func TestComputePlacesLayersLeftToRightAndCentresRows(t *testing.T) {
	layers := [][]string{{"start"}, {"a", "b"}, {"end"}}
	layout := Compute(layers, DefaultOptions())
	o := DefaultOptions()

	wantWidth := o.Margin*2 + 3*o.NodeWidth + 2*o.HorizontalGap
	wantHeight := o.Margin*2 + 2*o.NodeHeight + o.VerticalGap
	if !approx(layout.Size.Width, wantWidth) || !approx(layout.Size.Height, wantHeight) {
		t.Fatalf("size = %+v, want %vx%v", layout.Size, wantWidth, wantHeight)
	}
	start := layout.Positions["start"]
	if !approx(start.X, o.Margin) {
		t.Fatalf("start.X = %v, want %v", start.X, o.Margin)
	}
	// A single node is centred on the two-row layer.
	if !approx(start.Y, (wantHeight-o.NodeHeight)/2) {
		t.Fatalf("start.Y = %v, want centred", start.Y)
	}
	a, b := layout.Positions["a"], layout.Positions["b"]
	if !approx(a.X, o.Margin+o.NodeWidth+o.HorizontalGap) || a.X != b.X {
		t.Fatalf("second layer x positions wrong: a=%v b=%v", a, b)
	}
	if !approx(b.Y-a.Y, o.NodeHeight+o.VerticalGap) {
		t.Fatalf("rows should be stacked with the vertical gap: a=%v b=%v", a, b)
	}
	end := layout.Positions["end"]
	if !approx(end.X, o.Margin+2*(o.NodeWidth+o.HorizontalGap)) {
		t.Fatalf("end.X = %v", end.X)
	}
}

func TestComputeScalesWithZoom(t *testing.T) {
	layers := [][]string{{"s"}, {"t"}}
	full := Compute(layers, DefaultOptions())
	opts := DefaultOptions()
	opts.Zoom = 0.5
	half := Compute(layers, opts)
	if !approx(half.Size.Width*2, full.Size.Width) || !approx(half.NodeSize.Height*2, full.NodeSize.Height) {
		t.Fatalf("half zoom should halve the geometry: %+v vs %+v", half.Size, full.Size)
	}
	if !approx(half.Positions["t"].X*2, full.Positions["t"].X) {
		t.Fatalf("positions should scale with zoom")
	}
}

func TestComputeEmpty(t *testing.T) {
	layout := Compute(nil, DefaultOptions())
	if len(layout.Positions) != 0 || layout.Size.Width <= 0 {
		t.Fatalf("empty layout should still have margins: %+v", layout)
	}
}

func TestClampZoom(t *testing.T) {
	if ClampZoom(0.01) != MinZoom || ClampZoom(10) != MaxZoom || ClampZoom(1) != 1 {
		t.Fatalf("ClampZoom bounds are wrong")
	}
}

func TestFitZoom(t *testing.T) {
	layers := [][]string{{"a"}, {"b"}, {"c"}, {"d"}}
	natural := Compute(layers, DefaultOptions()).Size
	if zoom := FitZoom(layers, Size{natural.Width * 2, natural.Height * 2}, DefaultOptions()); zoom != 1 {
		t.Fatalf("large viewport should cap at 100%%, got %v", zoom)
	}
	zoom := FitZoom(layers, Size{natural.Width / 2, natural.Height * 3}, DefaultOptions())
	if !approx(zoom, 0.5) {
		t.Fatalf("expected 0.5 zoom to fit width, got %v", zoom)
	}
	if zoom := FitZoom(layers, Size{1, 1}, DefaultOptions()); zoom != MinZoom {
		t.Fatalf("tiny viewport should clamp to MinZoom, got %v", zoom)
	}
	if zoom := FitZoom(layers, Size{}, DefaultOptions()); zoom != 1 {
		t.Fatalf("unknown viewport should return 1, got %v", zoom)
	}
}

func TestRouteForwardEdge(t *testing.T) {
	layout := Compute([][]string{{"a"}, {"b"}}, DefaultOptions())
	path := Route(layout.Positions["a"], layout.Positions["b"], layout, 0)
	if len(path.Points) != 4 || path.Arrow != ArrowRight {
		t.Fatalf("forward edge should be a 4-point elbow ending with a right arrow: %+v", path)
	}
	first, last := path.Points[0], path.Points[3]
	if !approx(first.X, layout.Positions["a"].X+layout.NodeSize.Width) || !approx(last.X, layout.Positions["b"].X) {
		t.Fatalf("forward edge should connect right edge to left edge: %+v", path.Points)
	}
	if !approx(first.Y, layout.Positions["a"].Y+layout.NodeSize.Height/2) {
		t.Fatalf("forward edge should start at the vertical centre: %+v", first)
	}
}

func TestRouteSelfLoopAndBackwardEdge(t *testing.T) {
	layout := Compute([][]string{{"a"}, {"b"}}, DefaultOptions())
	loop := Route(layout.Positions["a"], layout.Positions["a"], layout, 0)
	if len(loop.Points) != 5 || loop.Arrow != ArrowDown {
		t.Fatalf("self loop should have 5 points ending downward: %+v", loop)
	}
	if loop.Points[2].Y >= layout.Positions["a"].Y {
		t.Fatalf("self loop should detour above the node: %+v", loop.Points)
	}
	back := Route(layout.Positions["b"], layout.Positions["a"], layout, 0)
	if len(back.Points) != 5 || back.Arrow != ArrowDown {
		t.Fatalf("backward edge should have 5 points ending downward: %+v", back)
	}
	tip := back.Points[len(back.Points)-1]
	if !approx(tip.X, layout.Positions["a"].X+layout.NodeSize.Width/2) || !approx(tip.Y, layout.Positions["a"].Y) {
		t.Fatalf("backward edge should enter the target's top centre: %+v", tip)
	}
}

func TestRouteOffsetShiftsAttachment(t *testing.T) {
	layout := Compute([][]string{{"a"}, {"b"}}, DefaultOptions())
	plain := Route(layout.Positions["a"], layout.Positions["b"], layout, 0)
	shifted := Route(layout.Positions["a"], layout.Positions["b"], layout, 9)
	if !approx(shifted.Points[0].Y-plain.Points[0].Y, 9) {
		t.Fatalf("offset should move the start point vertically")
	}
}

func TestArrowHead(t *testing.T) {
	tip := Point{100, 50}
	right := ArrowHead(tip, ArrowRight, 1)
	if right[0][1].X != 95 || right[1][1].X != 95 || right[0][1].Y != 45 || right[1][1].Y != 55 {
		t.Fatalf("unexpected right arrowhead: %+v", right)
	}
	down := ArrowHead(tip, ArrowDown, 2)
	if down[0][1].Y != 40 || down[1][1].Y != 40 || down[0][1].X != 90 || down[1][1].X != 110 {
		t.Fatalf("unexpected down arrowhead: %+v", down)
	}
}
