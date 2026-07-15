package cli

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const sessionTUIBackgroundProbeMaxBytes = 4096

type sessionTUIRGB struct {
	red   uint8
	green uint8
	blue  uint8
}

type sessionTUIDefaultColors struct {
	foreground sessionTUIRGB
	background sessionTUIRGB
}

func parseSessionTUIDefaultColors(buffer []byte) *sessionTUIDefaultColors {
	foreground, ok := parseSessionTUIOSCColor(buffer, 10)
	if !ok {
		return nil
	}
	background, ok := parseSessionTUIOSCColor(buffer, 11)
	if !ok {
		return nil
	}
	return &sessionTUIDefaultColors{foreground: foreground, background: background}
}

func parseSessionTUIOSCColor(buffer []byte, slot int) (sessionTUIRGB, bool) {
	prefix := []byte("\x1b]" + strconv.Itoa(slot) + ";")
	start := bytes.Index(buffer, prefix)
	if start < 0 {
		return sessionTUIRGB{}, false
	}
	payload := buffer[start+len(prefix):]
	end := -1
	for index, value := range payload {
		if value == '\a' || value == '\x1b' && index+1 < len(payload) && payload[index+1] == '\\' {
			end = index
			break
		}
	}
	if end < 0 {
		return sessionTUIRGB{}, false
	}
	return parseSessionTUIOSCRGB(string(payload[:end]))
}

func parseSessionTUIOSCRGB(payload string) (sessionTUIRGB, bool) {
	prefix, values, ok := strings.Cut(strings.TrimSpace(payload), ":")
	if !ok || !strings.EqualFold(prefix, "rgb") && !strings.EqualFold(prefix, "rgba") {
		return sessionTUIRGB{}, false
	}
	parts := strings.Split(values, "/")
	wantParts := 3
	if strings.EqualFold(prefix, "rgba") {
		wantParts = 4
	}
	if len(parts) != wantParts {
		return sessionTUIRGB{}, false
	}
	components := make([]uint8, len(parts))
	for index, part := range parts {
		component, ok := parseSessionTUIOSCComponent(part)
		if !ok {
			return sessionTUIRGB{}, false
		}
		components[index] = component
	}
	return sessionTUIRGB{red: components[0], green: components[1], blue: components[2]}, true
}

func parseSessionTUIOSCComponent(component string) (uint8, bool) {
	switch len(component) {
	case 2:
		value, err := strconv.ParseUint(component, 16, 8)
		return uint8(value), err == nil
	case 4:
		value, err := strconv.ParseUint(component, 16, 16)
		return uint8(value / 257), err == nil
	default:
		return 0, false
	}
}

func sessionTUIUserMessageBackground(background sessionTUIRGB) string {
	top := sessionTUIRGB{red: 255, green: 255, blue: 255}
	alpha := float32(0.12)
	if sessionTUIBackgroundIsLight(background) {
		top = sessionTUIRGB{}
		alpha = 0.04
	}
	blended := sessionTUIBlendColor(top, background, alpha)
	return fmt.Sprintf("#%02x%02x%02x", blended.red, blended.green, blended.blue)
}

func sessionTUIBackgroundIsLight(background sessionTUIRGB) bool {
	luminance := 0.299*float32(background.red) + 0.587*float32(background.green) + 0.114*float32(background.blue)
	return luminance > 128
}

func sessionTUIBlendColor(foreground, background sessionTUIRGB, alpha float32) sessionTUIRGB {
	blend := func(foreground, background uint8) uint8 {
		return uint8(float32(foreground)*alpha + float32(background)*(1-alpha))
	}
	return sessionTUIRGB{
		red:   blend(foreground.red, background.red),
		green: blend(foreground.green, background.green),
		blue:  blend(foreground.blue, background.blue),
	}
}
