package media

import (
	"testing"

	"github.com/Allan-Nava/segcheck/internal/media/mediatest"
)

// A `pssh` box says which DRM system can unlock the content, by UUID. A ladder
// whose MPD advertises PlayReady and whose CMAF init carries only a Widevine
// pssh plays on Chrome and dies on Xbox and Edge — and the manifest reads
// perfectly on the way down, so nothing but the init settles it.
func TestParse_EnumeratesDRMSystemsFromPSSH(t *testing.T) {
	init := mediatest.MP4InitCENCWithPSSH(1, 90000, 640, 360, "avc1", "cenc",
		mediatest.WidevineSystemID, mediatest.PlayReadySystemID)
	seg := mediatest.MP4Segment(1, 0, 0, 3600, 10, 500)

	info, err := Parse(seg, init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := info.DRMSystems
	if len(got) != 2 {
		t.Fatalf("found %d DRM systems, want 2: %v", len(got), got)
	}
	// Named, because a UUID in a finding is unreadable and an operator argues
	// about "PlayReady", not about 9a04f079.
	want := map[string]bool{"widevine": false, "playready": false}
	for _, s := range got {
		if _, ok := want[s.Name]; !ok {
			t.Errorf("unexpected system %q (%s)", s.Name, s.SystemID)
			continue
		}
		want[s.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("%s was not found in the init", name)
		}
	}
	if got[0].SystemID != mediatest.WidevineSystemID {
		t.Errorf("system UUID = %q, want %q in the order the init lists them", got[0].SystemID, mediatest.WidevineSystemID)
	}
}

// A system segcheck does not have a name for is still reported, by UUID. The
// list of DRM systems is not closed, and dropping an unknown one would report
// "no DRM in the init" about content that is protected.
func TestParse_UnknownDRMSystemIsReportedByUUID(t *testing.T) {
	const madeUp = "11111111-2222-3333-4444-555555555555"
	init := mediatest.MP4InitCENCWithPSSH(1, 90000, 640, 360, "avc1", "cenc", madeUp)

	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500), init)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(info.DRMSystems) != 1 {
		t.Fatalf("found %d systems, want 1", len(info.DRMSystems))
	}
	if info.DRMSystems[0].SystemID != madeUp {
		t.Errorf("SystemID = %q, want %q", info.DRMSystems[0].SystemID, madeUp)
	}
	if info.DRMSystems[0].Name != "" {
		t.Errorf("an unknown UUID was given the name %q; a guessed name is worse than none", info.DRMSystems[0].Name)
	}
}

// Unprotected content must gain no systems at all, so "the init carries none"
// is a statement rather than a default.
func TestParse_NoPSSHMeansNoDRMSystems(t *testing.T) {
	info, err := Parse(mediatest.MP4Segment(1, 0, 0, 3600, 10, 500),
		mediatest.MP4Init(1, 90000, "video", 640, 360))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(info.DRMSystems) != 0 {
		t.Errorf("unprotected content reported %d DRM systems", len(info.DRMSystems))
	}
}
