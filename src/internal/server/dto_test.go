package server

import (
	"testing"

	"github.com/TokenCemetery/couch/internal/media"
)

// HDR10 and Dolby Vision 8.1 report what the reference Emby does for the same
// streams (#125); SDR and audio keep None.
func TestMediaStreamsReportDynamicRange(t *testing.T) {
	hdr := media.Stream{Type: "Video", Codec: "hevc", Height: 1080, ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", ColorSpace: "bt2020nc"}
	dolby := hdr
	dolby.Height, dolby.DoviProfile, dolby.DoviCompatibility = 2160, 8, 1
	hlg := hdr
	hlg.ColorTransfer = "arib-std-b67"
	sdr := media.Stream{Type: "Video", Codec: "h264", Height: 1080, ColorTransfer: "bt709"}
	streams := mediaStreams(media.Item{Streams: []media.Stream{hdr, dolby, hlg, sdr, {Type: "Audio", Codec: "aac"}}})
	for i, want := range []object{
		{"VideoRange": "HDR 10", "ExtendedVideoType": "Hdr10", "ExtendedVideoSubType": "Hdr10", "ExtendedVideoSubTypeDescription": "HDR 10",
			"ColorTransfer": "smpte2084", "ColorPrimaries": "bt2020", "ColorSpace": "bt2020nc", "DisplayTitle": "HEVC 1080p HDR 10"},
		{"VideoRange": "DolbyVision", "ExtendedVideoType": "DolbyVision", "ExtendedVideoSubType": "DoviProfile81",
			"ExtendedVideoSubTypeDescription": "Profile 8.1 (HDR10 compatible)", "DisplayTitle": "HEVC 4K Dolby Vision"},
		{"VideoRange": "HLG", "ExtendedVideoType": "HyperLogGamma", "ColorTransfer": "arib-std-b67"},
		{"VideoRange": "SDR", "ExtendedVideoType": "None", "ExtendedVideoSubType": "None", "ColorTransfer": "bt709", "DisplayTitle": "H264 1080p"},
		{"ExtendedVideoType": "None", "ExtendedVideoSubType": "None", "ExtendedVideoSubTypeDescription": "None"},
	} {
		for key, value := range want {
			if streams[i][key] != value {
				t.Errorf("stream %d %s: %v, want %v", i, key, streams[i][key], value)
			}
		}
	}
	if _, found := streams[4]["VideoRange"]; found {
		t.Error("audio stream has a VideoRange")
	}
}
