package config_test

import (
	"strings"
	"testing"

	"github.com/antimatter-studios/vertrag/config"
)

func TestTheSurfaceSectionIsRead(t *testing.T) {
	path := writeConfig(t, "vertrag.yml", `
spec: ./api.yml
endpoint: http://localhost:4210
surface:
  require: 95.5
  exclude:
    - operation: POST /admin/reindex
      reason: an operator task
    - operation: deleteWidget
      reason: covered by the lifecycle suite
`)

	settings, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings.Surface.Require != 95.5 {
		t.Errorf("require = %v, want 95.5", settings.Surface.Require)
	}
	want := []config.SurfaceExclusion{
		{Operation: "POST /admin/reindex", Reason: "an operator task"},
		{Operation: "deleteWidget", Reason: "covered by the lifecycle suite"},
	}
	if len(settings.Surface.Exclude) != len(want) {
		t.Fatalf("exclude = %+v, want %+v", settings.Surface.Exclude, want)
	}
	for i := range want {
		if settings.Surface.Exclude[i] != want[i] {
			t.Errorf("exclude[%d] = %+v, want %+v", i, settings.Surface.Exclude[i], want[i])
		}
	}
}

// TestASurfaceSettingThatCannotMeanWhatItSaysIsRefused is refused at load,
// before the first request, rather than discovered from an exit code at the
// end of a long suite.
func TestASurfaceSettingThatCannotMeanWhatItSaysIsRefused(t *testing.T) {
	for _, test := range []struct {
		name, section, want string
	}{
		{"a gate that cannot pass", "require: 101", "from 0 to 100"},
		{"a gate that cannot fail", "require: -1", "from 0 to 100"},
		{"a gate that is not a number", "require: .nan", "from 0 to 100"},
		{"an exclusion with no reason", "exclude: [{operation: GET /x}]", "gives no reason"},
		{"an exclusion with a blank reason", "exclude: [{operation: GET /x, reason: '  '}]", "gives no reason"},
		{"an exclusion naming nothing", "exclude: [{reason: because}]", "names no operation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeConfig(t, "vertrag.yml", "spec: ./api.yml\nsurface:\n  "+test.section+"\n")
			_, err := config.Load(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("err = %v, want one saying %q", err, test.want)
			}
		})
	}
}
