package ui

import (
	"image"
	"image/color"
	"math"
)

// SetPixel sets a single pixel with bounds checking.
func SetPixel(img *image.RGBA, x, y int, col color.RGBA) {
	if x >= img.Rect.Min.X && x < img.Rect.Max.X && y >= img.Rect.Min.Y && y < img.Rect.Max.Y {
		img.SetRGBA(x, y, col)
	}
}

// DrawHLine draws a horizontal line from x0 to x1 (inclusive) at y.
func DrawHLine(img *image.RGBA, x0, x1, y int, col color.RGBA) {
	if x0 > x1 {
		x0, x1 = x1, x0
	}
	for x := x0; x <= x1; x++ {
		SetPixel(img, x, y, col)
	}
}

// DrawVLine draws a vertical line from y0 to y1 (inclusive) at x.
func DrawVLine(img *image.RGBA, x, y0, y1 int, col color.RGBA) {
	if y0 > y1 {
		y0, y1 = y1, y0
	}
	for y := y0; y <= y1; y++ {
		SetPixel(img, x, y, col)
	}
}

// DrawLine draws a line using Bresenham's algorithm.
func DrawLine(img *image.RGBA, x0, y0, x1, y1 int, col color.RGBA) {
	dx := x1 - x0
	dy := y1 - y0
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}

	sx := 1
	if x0 > x1 {
		sx = -1
	}
	sy := 1
	if y0 > y1 {
		sy = -1
	}

	err := dx - dy

	for {
		SetPixel(img, x0, y0, col)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 > -dy {
			err -= dy
			x0 += sx
		}
		if e2 < dx {
			err += dx
			y0 += sy
		}
	}
}

// DrawLineThick draws a thick line by offsetting parallel Bresenham lines.
func DrawLineThick(img *image.RGBA, x0, y0, x1, y1, thickness int, col color.RGBA) {
	if thickness <= 1 {
		DrawLine(img, x0, y0, x1, y1, col)
		return
	}

	dx := float64(x1 - x0)
	dy := float64(y1 - y0)
	length := math.Sqrt(dx*dx + dy*dy)
	if length == 0 {
		// Single point
		half := thickness / 2
		for ox := -half; ox <= half; ox++ {
			for oy := -half; oy <= half; oy++ {
				SetPixel(img, x0+ox, y0+oy, col)
			}
		}
		return
	}

	// Perpendicular unit vector
	px := -dy / length
	py := dx / length

	half := float64(thickness-1) / 2.0
	for i := -half; i <= half; i += 1.0 {
		ox := int(math.Round(px * i))
		oy := int(math.Round(py * i))
		DrawLine(img, x0+ox, y0+oy, x1+ox, y1+oy, col)
	}
}

// DrawArc draws an arc centered at (cx,cy) with given radius, from startDeg to endDeg (clockwise in screen coords).
func DrawArc(img *image.RGBA, cx, cy, radius int, startDeg, endDeg float64, thickness int, col color.RGBA) {
	// Step through angles at sub-degree resolution for smooth arcs
	step := 0.5
	half := float64(thickness) / 2.0

	for deg := startDeg; deg <= endDeg; deg += step {
		rad := deg * math.Pi / 180.0
		cosA := math.Cos(rad)
		sinA := math.Sin(rad)

		// Fill a small block at each angle to create thickness
		for t := -half; t <= half; t += 1.0 {
			r := float64(radius) + t
			x := cx + int(math.Round(r*cosA))
			y := cy + int(math.Round(r*sinA))
			SetPixel(img, x, y, col)
		}
	}
}
