/*
 * ChatCLI - Startup banner gradient
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The block letters of the startup banner are painted column by column with
 * a left-to-right gradient, the same one the documentation site draws its
 * banner with. The default themes use the ChatCLI brand ramp (teal, cyan,
 * blue); every other theme runs from its own Primary to its Secondary, so a
 * theme switch still re-skins the banner.
 *
 * The gradient needs per-cell colors, so it is emitted only where the
 * terminal can show them: truecolor gets the exact ramp and 256-color gets
 * the nearest xterm palette entry for each column. A 16-color terminal, a
 * NO_COLOR terminal and a pipe keep the single-color banner they always had,
 * byte for byte.
 */

package cli

import (
	"fmt"
	"math"
	"strings"

	"github.com/diillson/chatcli/ui/theme"
)

// bannerRamp is a gradient as three stops: start, middle and end.
type bannerRamp [3][3]float64

// Brand ramps, matching the docs site (style.css, --cb-a/--cb-b/--cb-c).
var (
	bannerRampDark  = bannerRamp{{45, 212, 191}, {34, 211, 238}, {96, 165, 250}}
	bannerRampLight = bannerRamp{{13, 148, 136}, {8, 145, 178}, {37, 99, 235}}
)

// bannerRampFor picks the ramp for a theme: the brand ramp for the two
// default themes, the theme's own Primary -> Secondary for the others.
func bannerRampFor(t theme.Theme) bannerRamp {
	switch t.Name {
	case "dark":
		return bannerRampDark
	case "light":
		return bannerRampLight
	}
	a, okA := hexRGB(t.Palette.Primary.Hex)
	c, okC := hexRGB(t.Palette.Secondary.Hex)
	if !okA || !okC {
		if t.Variant == theme.VariantLight {
			return bannerRampLight
		}
		return bannerRampDark
	}
	var mid [3]float64
	for i := range mid {
		mid[i] = (a[i] + c[i]) / 2
	}
	return bannerRamp{a, mid, c}
}

// at returns the ramp's color at t in [0,1]; the middle stop sits at 0.55,
// as on the docs site.
func (r bannerRamp) at(t float64) [3]float64 {
	t = math.Max(0, math.Min(1, t))
	from, to, u := r[0], r[1], t/0.55
	if t > 0.55 {
		from, to, u = r[1], r[2], (t-0.55)/0.45
	}
	var out [3]float64
	for i := range out {
		out[i] = from[i] + (to[i]-from[i])*u
	}
	return out
}

// hexRGB parses "#RRGGBB".
func hexRGB(h string) ([3]float64, bool) {
	var r, g, b uint8
	if len(h) != 7 || h[0] != '#' {
		return [3]float64{}, false
	}
	if _, err := fmt.Sscanf(h[1:], "%02x%02x%02x", &r, &g, &b); err != nil {
		return [3]float64{}, false
	}
	return [3]float64{float64(r), float64(g), float64(b)}, true
}

// bannerSGR is the foreground escape for c under profile p, or "" when the
// profile cannot show a per-column gradient.
func bannerSGR(c [3]float64, p theme.Profile) string {
	r, g, b := clampByte(c[0]), clampByte(c[1]), clampByte(c[2])
	switch p {
	case theme.ProfileTrueColor:
		return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
	case theme.ProfileANSI256:
		return fmt.Sprintf("\033[38;5;%dm", xterm256(r, g, b))
	default:
		return ""
	}
}

func clampByte(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// xterm256 maps a color to the nearest entry of the xterm 6x6x6 cube or its
// grayscale ramp (indices 16-255), whichever is closer.
func xterm256(r, g, b uint8) int {
	levels := [6]int{0, 95, 135, 175, 215, 255}
	nearest := func(v uint8) int {
		best, bestD := 0, 1<<30
		for i, l := range levels {
			if d := (int(v) - l) * (int(v) - l); d < bestD {
				best, bestD = i, d
			}
		}
		return best
	}
	ri, gi, bi := nearest(r), nearest(g), nearest(b)
	cube := 16 + 36*ri + 6*gi + bi
	dist := func(x, y, z int) int {
		return (int(r)-x)*(int(r)-x) + (int(g)-y)*(int(g)-y) + (int(b)-z)*(int(b)-z)
	}
	cubeD := dist(levels[ri], levels[gi], levels[bi])
	avg := (int(r) + int(g) + int(b)) / 3
	gi24 := (avg - 8 + 5) / 10
	if gi24 < 0 {
		gi24 = 0
	} else if gi24 > 23 {
		gi24 = 23
	}
	gv := 8 + 10*gi24
	if dist(gv, gv, gv) < cubeD {
		return 232 + gi24
	}
	return cube
}

// gradientLogo paints the block cells of each logo row by column and the
// box-drawing strokes with the muted border color. It returns ok=false when
// the active profile cannot show a gradient, so the caller keeps the classic
// single-color banner.
func gradientLogo(lines []string, p theme.Profile, ramp bannerRamp) ([]string, bool) {
	if p != theme.ProfileTrueColor && p != theme.ProfileANSI256 {
		return nil, false
	}
	cols := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > cols {
			cols = n
		}
	}
	if cols < 2 {
		return nil, false
	}
	edge := colorize("", ColorGray)                // the themed muted escape + reset
	edgeOn := strings.TrimSuffix(edge, ColorReset) // just the escape
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var b strings.Builder
		for i, ch := range []rune(line) {
			switch {
			case ch == '█':
				b.WriteString(bannerSGR(ramp.at(float64(i)/float64(cols-1)), p))
				b.WriteRune(ch)
				b.WriteString(ColorReset)
			case strings.ContainsRune("╔╗╚╝═║", ch):
				b.WriteString(edgeOn)
				b.WriteRune(ch)
				b.WriteString(ColorReset)
			default:
				b.WriteRune(ch)
			}
		}
		out = append(out, b.String())
	}
	return out, true
}
