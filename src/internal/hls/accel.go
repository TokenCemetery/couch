package hls

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Accel is the hardware H.264 encoder of a transcode; the zero value
// encodes with libx264. Decoding and filters stay on the CPU, so every
// source and filter that libx264 handles reaches the hardware encoder.
type Accel struct {
	// Kind is "vaapi", "qsv", "nvenc" or "amf".
	Kind string
	// Device is the DRM render node for vaapi and qsv.
	Device string
}

// Accels lists the supported Accel kinds.
var Accels = []string{"vaapi", "qsv", "nvenc", "amf"}

// quality is the encoders' target quality, on x264's CRF scale.
const quality = "23"

// Encoder is the FFmpeg encoder name.
func (a Accel) Encoder() string {
	if a.Kind == "" {
		return "libx264"
	}
	return "h264_" + a.Kind
}

// global returns the options, placed before the input, that open the
// device the upload filter needs.
func (a Accel) global() []string {
	switch a.Kind {
	case "vaapi":
		return []string{"-vaapi_device", a.Device}
	case "qsv":
		return []string{"-init_hw_device", "qsv=hw:hw_any,child_device=" + a.Device, "-filter_hw_device", "hw"}
	}
	return nil
}

// upload returns the filters, appended to the video chain, that move the
// frames to the device; nvenc and amf read system memory.
func (a Accel) upload() string {
	switch a.Kind {
	case "vaapi":
		return ",format=nv12,hwupload"
	case "qsv":
		return ",format=nv12,hwupload=extra_hw_frames=64"
	}
	return ""
}

// encode returns the encoder options: quality-targeted rate control capped
// at bitrate when it is positive, and forced keyframes encoded as IDR, so
// that every segment starts with one. The libx264 options are Emby's: they
// cut motion search and lookahead, a third less CPU for a larger file at the
// same CRF (#99). The hardware ones are checked on VA-API (radeonsi) only.
func (a Accel) encode(bitrate int64) []string {
	rate := strconv.FormatInt(bitrate, 10)
	buffer := strconv.FormatInt(2*bitrate, 10)
	var args []string
	switch a.Kind {
	case "":
		args = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", quality, "-x264-params", "subme=0:me_range=4:rc_lookahead=10:partitions=none", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			args = append(args, "-maxrate", rate, "-bufsize", buffer)
		}
	case "vaapi":
		// With a bitrate the driver picks QVBR, or VBR without it; a forced
		// keyframe is always an IDR frame.
		args = []string{"-c:v", "h264_vaapi", "-global_quality", quality}
		if bitrate > 0 {
			args = append(args, "-b:v", rate, "-maxrate", rate, "-bufsize", buffer)
		}
	case "qsv":
		// QVBR needs a target below the cap: equal ones select CBR.
		args = []string{"-c:v", "h264_qsv", "-preset", "veryfast", "-global_quality", quality, "-forced_idr", "1"}
		if bitrate > 0 {
			args = append(args, "-b:v", strconv.FormatInt(bitrate/2, 10), "-maxrate", rate, "-bufsize", buffer)
		}
	case "nvenc":
		args = []string{"-c:v", "h264_nvenc", "-preset", "p4", "-rc", "vbr", "-cq", quality, "-b:v", "0", "-forced-idr", "1", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			args = append(args, "-maxrate", rate, "-bufsize", buffer)
		}
	case "amf":
		args = []string{"-c:v", "h264_amf", "-quality", "speed", "-rc", "qvbr", "-qvbr_quality_level", quality, "-forced_idr", "1", "-pix_fmt", "yuv420p"}
		if bitrate > 0 {
			args = append(args, "-b:v", rate, "-maxrate", rate, "-bufsize", buffer)
		}
	}
	return args
}

// CheckAccel encodes a few frames of a test pattern with m.Accel, so that a
// missing device, driver or encoder stops startup instead of every
// transcode.
func (m *Manager) CheckAccel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := append([]string{"-v", "error", "-nostdin"}, m.Accel.global()...)
	args = append(args, "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=25:duration=0.4",
		"-vf", "scale=w=iw:h=ih"+m.Accel.upload())
	args = append(args, m.Accel.encode(4_000_000)...)
	args = append(args, "-f", "null", "-")
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, m.ffmpeg, args...) //nolint:gosec // binary is ffmpeg from PATH; the device is the operator's -hwaccel-device
	cmd.Env = []string{"LC_ALL=C"}
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			first, _, _ := strings.Cut(msg, "\n")
			return fmt.Errorf("%s: %w: %s", m.Accel.Encoder(), err, first)
		}
		return fmt.Errorf("%s: %w", m.Accel.Encoder(), err)
	}
	return nil
}
