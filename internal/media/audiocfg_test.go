package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// An fMP4 audio track's real configuration is not in the AudioSampleEntry, it is
// in the box beside it: `esds` for AAC, `dOps` for Opus, `dfLa` for FLAC — the
// same split video already has, where the container states it and no bitstream
// reader is needed.
//
// For AAC the AudioSpecificConfig inside `esds` is the only place three things
// are written down: which object type, which sampling frequency, and which
// channel configuration. The sample entry's own channelcount and rate are
// famously not evidence — HE-AAC codes at half the rate it plays and HE-AAC v2
// codes a mono core it renders as stereo — so the entry can be right about the
// output and say nothing about the coding.
func TestParse_AudioSpecificConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		objectType int
		freqIndex  int
		channelCfg int
		sbr        bool
		ps         bool
		wantAOT    int
		wantRate   int
		wantChans  int
		wantSBR    bool
		wantPS     bool
	}{
		{
			name:       "AAC-LC stereo at 48 kHz",
			objectType: 2, freqIndex: 3, channelCfg: 2,
			wantAOT: 2, wantRate: 48000, wantChans: 2,
		},
		{
			name:       "HE-AAC: an SBR extension over a 24 kHz core",
			objectType: 5, freqIndex: 6, channelCfg: 2, sbr: true,
			wantAOT: 5, wantRate: 24000, wantChans: 2, wantSBR: true,
		},
		{
			name:       "HE-AAC v2: Parametric Stereo over a mono core",
			objectType: 29, freqIndex: 6, channelCfg: 1, sbr: true, ps: true,
			wantAOT: 29, wantRate: 24000, wantChans: 1, wantSBR: true, wantPS: true,
		},
		{
			name:       "5.1",
			objectType: 2, freqIndex: 3, channelCfg: 6,
			wantAOT: 2, wantRate: 48000, wantChans: 6,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := mediatest.MP4InitESDS(1, 48000, tc.objectType, tc.freqIndex, tc.channelCfg, tc.sbr, tc.ps)
			info, err := Parse(mediatest.MP4Segment(1, 0, 0, 1024, 10, 500), init)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			track, ok := info.Track(Audio)
			if !ok {
				t.Fatal("no audio track")
			}
			cfg, ok := track.AudioConfig()
			if !ok {
				t.Fatal("the sample entry carries an esds and no configuration was read")
			}
			if cfg.ObjectType != tc.wantAOT {
				t.Errorf("ObjectType = %d, want %d", cfg.ObjectType, tc.wantAOT)
			}
			// The *coded* rate and channel count, which is what the
			// AudioSpecificConfig states and what a codec string has to agree
			// with — not what the track renders.
			if cfg.CodedSampleRate != tc.wantRate {
				t.Errorf("CodedSampleRate = %d, want %d", cfg.CodedSampleRate, tc.wantRate)
			}
			if cfg.CodedChannels != tc.wantChans {
				t.Errorf("CodedChannels = %d, want %d", cfg.CodedChannels, tc.wantChans)
			}
			if cfg.SBR != tc.wantSBR || cfg.PS != tc.wantPS {
				t.Errorf("SBR/PS = %v/%v, want %v/%v", cfg.SBR, cfg.PS, tc.wantSBR, tc.wantPS)
			}
		})
	}
}

// Opus states its channel count in `dOps`, and it always plays at 48 kHz
// whatever the original material was.
func TestParse_OpusConfig(t *testing.T) {
	init := mediatest.MP4InitDOps(1, 48000, 2, 48000)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 960, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	cfg, ok := track.AudioConfig()
	if !ok {
		t.Fatal("the sample entry carries a dOps and no configuration was read")
	}
	if cfg.CodedChannels != 2 || cfg.CodedSampleRate != 48000 {
		t.Errorf("Opus config = %d channels at %d Hz, want 2 at 48000", cfg.CodedChannels, cfg.CodedSampleRate)
	}
}

// FLAC states both in the STREAMINFO block inside `dfLa`.
func TestParse_FLACConfig(t *testing.T) {
	init := mediatest.MP4InitDFLA(1, 44100, 2, 44100)
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 4096, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, ok := info.Track(Audio)
	if !ok {
		t.Fatal("no audio track")
	}
	cfg, ok := track.AudioConfig()
	if !ok {
		t.Fatal("the sample entry carries a dfLa and no configuration was read")
	}
	if cfg.CodedChannels != 2 || cfg.CodedSampleRate != 44100 {
		t.Errorf("FLAC config = %d channels at %d Hz, want 2 at 44100", cfg.CodedChannels, cfg.CodedSampleRate)
	}
}

// A track whose sample entry carries no configuration box states nothing, and a
// zero channel count must not read as a claim of silence.
func TestParse_NoAudioConfigBoxStatesNothing(t *testing.T) {
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 1024, 10, 500),
		mediatest.MP4InitAudio(1, 48000, "mp4a", 2, 48000))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	track, _ := info.Track(Audio)
	if _, ok := track.AudioConfig(); ok {
		t.Error("a sample entry with no configuration box reported one")
	}
}
