package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/settings"
)

// glm-5.3-flash takes video/mp4 (verified by the reporter against the same
// model direct). A plugin's model list that names only text+image must not
// make magpie advertise no video, or clients reading /v1/models reject the
// file part before the request is ever forwarded (xinuxZ, #3715).
func TestGLM53FlashModalitiesKeepVideo(t *testing.T) {
	yes := true
	// the model list a plugin actually serves: it names text and image
	// only, leaving video/audio/pdf unstated
	m := catalog.Model{ID: "glm-5.3-flash", InputModalities: []string{"text", "image"}, ImageInput: &yes}
	for _, id := range []string{"trae-cn", "loomy"} {
		e := entryFor(Provider{ID: id}, m, settings.Settings{})
		in, _ := e.Modalities()
		if !slices.Contains(in, "video") {
			t.Errorf("%s/glm-5.3-flash: /v1/models input modalities = %v, want video among them", id, in)
		}
	}
}

// The known modalities are added without dropping what a partial source
// named; a model magpie knows nothing about keeps its fail-closed default.
func TestKnownModalitiesGLM53Flash(t *testing.T) {
	in, out := withKnownModalities("glm-5.3-flash", nil, nil)
	if !slices.Equal(in, []string{"text", "image", "audio", "video", "pdf"}) {
		t.Errorf("glm-5.3-flash in = %v", in)
	}
	if !slices.Equal(out, []string{"text"}) {
		t.Errorf("glm-5.3-flash out = %v", out)
	}
	in, _ = withKnownModalities("glm-5.3-flash", []string{"text", "image"}, nil)
	if !slices.Contains(in, "video") || !slices.Contains(in, "audio") || !slices.Contains(in, "pdf") {
		t.Errorf("glm-5.3-flash partial source lost known modalities: %v", in)
	}
	in, out = withKnownModalities("some-unknown-model", []string{"text"}, []string{"text"})
	if !slices.Equal(in, []string{"text"}) || !slices.Equal(out, []string{"text"}) {
		t.Errorf("unknown model changed: in %v out %v", in, out)
	}
}
