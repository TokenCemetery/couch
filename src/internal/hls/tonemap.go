package hls

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// ToneMap is how an encoded PQ or HLG video becomes SDR (#125).
type ToneMap string

const (
	// ToneMapOff encodes the HDR picture as it is and keeps its colour tags:
	// a client that ignores them shows it washed out. Emby 4.10.2.0 does the
	// same by default.
	ToneMapOff ToneMap = ""
	// ToneMapGPU tone-maps with libplacebo on a Vulkan device. libplacebo
	// refuses software Vulkan devices such as lavapipe, so it never runs on
	// the CPU.
	ToneMapGPU ToneMap = "libplacebo"
	// ToneMapSoftware tone-maps with zscale on the CPU, about a third of the
	// speed of an SDR encode.
	ToneMapSoftware ToneMap = "zscale"
)

// ToneMapModes lists the -tonemap modes SetToneMap accepts.
var ToneMapModes = []string{"auto", "software", "off"}

// pqOrHLG reports whether the video is tagged as PQ (HDR10, Dolby Vision
// with an HDR10 base layer) or HLG.
func (s Source) pqOrHLG() bool {
	return s.ColorTransfer == "smpte2084" || s.ColorTransfer == "arib-std-b67"
}

// hdr reports whether the source's video is HDR: PQ, HLG or Dolby Vision,
// whose profile 5 may carry no transfer tag.
func (s Source) hdr() bool {
	return s.pqOrHLG() || s.DoviProfile > 0
}

// method is how t tone-maps source: ToneMapOff for SDR, and for a video zscale
// cannot read, a Dolby Vision base layer that is neither HDR10 nor HLG
// (profile 5) or one without a transfer tag. libplacebo reads Dolby Vision
// from the decoded frames.
func (t ToneMap) method(source Source) ToneMap {
	switch {
	case !source.hdr():
		return ToneMapOff
	case t == ToneMapSoftware && (!source.pqOrHLG() || (source.DoviProfile > 0 && source.DoviCompatibility == 0)):
		return ToneMapOff
	}
	return t
}

// filter returns the filter chain that turns source's HDR video into 8-bit
// BT.709; empty when method is ToneMapOff. HDR video is BT.2020, so zscale
// takes those primaries and matrix rather than the probed ones.
func (t ToneMap) filter(source Source) string {
	switch t.method(source) {
	case ToneMapGPU:
		return "libplacebo=colorspace=bt709:color_primaries=bt709:color_trc=bt709:range=tv:format=yuv420p:tonemapping=auto"
	case ToneMapSoftware:
		return "zscale=tin=" + source.ColorTransfer + ":pin=bt2020:min=bt2020nc:p=bt2020:t=linear:npl=100,format=gbrpf32le," +
			"zscale=p=bt709,tonemap=tonemap=mobius:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
	}
	return ""
}

// logName is t as logs name it.
func (t ToneMap) logName() string {
	if t == ToneMapOff {
		return "off"
	}
	return string(t)
}

// SetToneMap chooses m.ToneMap for a -tonemap mode and logs the choice:
// "auto" takes ToneMapGPU when its check passes and ToneMapOff otherwise,
// "software" takes ToneMapSoftware instead of ToneMapOff and fails when its
// check does, and "off" takes ToneMapOff without a check.
func (m *Manager) SetToneMap(ctx context.Context, mode string) error {
	m.ToneMap = ToneMapOff
	switch mode {
	case "off":
	case "auto", "software":
		m.ToneMap = ToneMapGPU
		if err := m.CheckToneMap(ctx); err != nil {
			slog.Info("HDR tone mapping on the GPU is unavailable", "error", err)
			m.ToneMap = ToneMapOff
			if mode == "software" {
				m.ToneMap = ToneMapSoftware
				if err := m.CheckToneMap(ctx); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("unknown tone mapping mode %q", mode)
	}
	slog.Info("HDR tone mapping", "method", m.ToneMap.logName())
	return nil
}

// CheckToneMap tone-maps a few frames of a test pattern with m.ToneMap, so
// that a missing Vulkan device or zscale is found at startup.
func (m *Manager) CheckToneMap(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-nostdin", "-f", "lavfi", "-i", "testsrc2=size=256x144:rate=25:duration=0.2",
		"-vf", m.ToneMap.filter(Source{ColorTransfer: "smpte2084"}), "-f", "null", "-"}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, m.ffmpeg, args...) //nolint:gosec // binary is ffmpeg from PATH
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			first, _, _ := strings.Cut(msg, "\n")
			return fmt.Errorf("%s: %w: %s", m.ToneMap, err, first)
		}
		return fmt.Errorf("%s: %w", m.ToneMap, err)
	}
	return nil
}
