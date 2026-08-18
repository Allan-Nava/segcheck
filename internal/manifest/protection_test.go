package manifest

import (
	"testing"
	"time"
)

// What a manifest *promises* about protection is a list of systems and a
// scheme, and until now segcheck reduced it to a bool. A ladder that advertises
// PlayReady and ships a Widevine-only init plays on Chrome and dies on Xbox,
// and neither half of that claim survived being flattened.
func TestParseDASH_ContentProtectionSystemsAndScheme(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cbcs" cenc:default_KID="11111111-2222-3333-4444-555555555555" xmlns:cenc="urn:mpeg:cenc:2013"/>
    <ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"/>
    <ContentProtection schemeIdUri="urn:uuid:9a04f079-9840-4286-ab92-e65be0885f95"/>
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]

	// The scheme is what mp4protection's @value states, and it is the thing a
	// `cbcs` stream served as `cenc` gets wrong.
	if r.KeyScheme != "cbcs" {
		t.Errorf("KeyScheme = %q, want cbcs from the mp4protection value", r.KeyScheme)
	}
	// The systems are the urn:uuid entries, normalised to bare UUIDs so they can
	// be compared with what a pssh box carries.
	if len(r.DRMSystems) != 2 {
		t.Fatalf("declared %d DRM systems, want 2: %v", len(r.DRMSystems), r.DRMSystems)
	}
	if r.DRMSystems[0] != "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed" {
		t.Errorf("first system = %q, want the bare UUID", r.DRMSystems[0])
	}
	// mp4protection is not a DRM system; counting it as one would report a
	// system the init can never carry.
	for _, s := range r.DRMSystems {
		if s == "urn:mpeg:dash:mp4protection:2011" || s == "" {
			t.Errorf("mp4protection was counted as a DRM system: %v", r.DRMSystems)
		}
	}
}

// DASH-IF's own guidance puts the key-acquisition data in the MPD, inside a
// cenc:pssh element, and every real multi-DRM vector does exactly that — so the
// initialisation segment legitimately carries no pssh at all. A check that
// demanded one in the init reported Axinom's reference stream as missing both
// its DRM systems.
func TestParseDASH_CencPSSHInTheManifest(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013" type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
    <ContentProtection schemeIdUri="urn:uuid:9a04f079-9840-4286-ab92-e65be0885f95">
      <cenc:pssh>AAAANHBzc2gAAAAA</cenc:pssh>
    </ContentProtection>
    <ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"/>
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if len(r.DRMSystems) != 2 {
		t.Fatalf("declared %d systems, want 2", len(r.DRMSystems))
	}
	// Only PlayReady's acquisition data is in the manifest; Widevine's has to
	// come from the init, and the difference is the whole point.
	if len(r.DRMInManifest) != 1 || r.DRMInManifest[0] != "9a04f079-9840-4286-ab92-e65be0885f95" {
		t.Errorf("DRMInManifest = %v, want just the PlayReady UUID", r.DRMInManifest)
	}
}

// An unprotected MPD must gain none of this: an empty scheme is "the manifest
// says nothing", and a check has to be able to tell that from "cenc".
func TestParseDASH_NoContentProtectionMeansNoClaims(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="static" mediaPresentationDuration="PT10S">
  <Period><AdaptationSet mimeType="video/mp4">
    <SegmentTemplate timescale="1" duration="4" media="v/$Number$.m4s" startNumber="1"/>
    <Representation id="v" bandwidth="800000" width="640" height="360"/>
  </AdaptationSet></Period>
</MPD>`
	pl, err := ParseDASH([]byte(mpd), "https://cdn.example/m.mpd", time.Now())
	if err != nil {
		t.Fatalf("ParseDASH: %v", err)
	}
	r := pl.Renditions[0]
	if r.KeyScheme != "" || len(r.DRMSystems) != 0 || len(r.DRMInManifest) != 0 || r.KeyMethod != "" {
		t.Errorf("an unprotected MPD gained protection claims: scheme=%q systems=%v method=%q",
			r.KeyScheme, r.DRMSystems, r.KeyMethod)
	}
}

// HLS names the system in KEYFORMAT, in three spellings the industry actually
// uses. Comparing them with a pssh needs them on one vocabulary.
func TestHLSKeyFormatNamesADRMSystem(t *testing.T) {
	for _, tc := range []struct{ keyformat, want string }{
		{"urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed", "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"},
		{"com.apple.streamingkeydelivery", "94ce86fb-07ff-4f43-adb8-93d2fa968ca2"},
		{"com.microsoft.playready", "9a04f079-9840-4286-ab92-e65be0885f95"},
		{"identity", ""},
		{"", ""},
	} {
		if got := DRMSystemForKeyFormat(tc.keyformat); got != tc.want {
			t.Errorf("DRMSystemForKeyFormat(%q) = %q, want %q", tc.keyformat, got, tc.want)
		}
	}
}
