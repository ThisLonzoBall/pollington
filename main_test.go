package main

import (
	"runtime/debug"
	"testing"
)

func TestVersionString(t *testing.T) {
	setting := func(k, v string) debug.BuildSetting {
		return debug.BuildSetting{Key: k, Value: v}
	}

	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{
			// A binary built with -buildvcs=false, or read by a toolchain that
			// has no build info at all.
			name: "no build info",
			ok:   false,
			want: "pollington (unknown version)",
		},
		{
			name: "installed from a tag",
			info: &debug.BuildInfo{Main: debug.Module{Version: "v0.3.1"}},
			ok:   true,
			want: "pollington v0.3.1",
		},
		{
			// The common case: built from a checkout. "(devel)" alone names no
			// particular build, so the commit is what makes this useful.
			name: "built from a clean checkout",
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{setting("vcs.revision", "8471080c0ffee1234567890abcdef")},
			},
			ok:   true,
			want: "pollington (devel) (8471080)",
		},
		{
			// Worth distinguishing: a report against a dirty tree cannot be
			// reproduced from the commit alone.
			name: "built from a dirty tree",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					setting("vcs.revision", "8471080c0ffee"),
					setting("vcs.modified", "true"),
				},
			},
			ok:   true,
			want: "pollington (devel) (8471080, dirty)",
		},
		{
			// go build from a checkout stamps a pseudo-version that already
			// carries the commit and the dirty marker; repeating them reads as
			// "v0.0.0-...-847108053d73+dirty (8471080, dirty)".
			name: "pseudo-version is not decorated twice",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "v0.0.0-20261022203949-847108053d73+dirty"},
				Settings: []debug.BuildSetting{
					setting("vcs.revision", "847108053d73"),
					setting("vcs.modified", "true"),
				},
			},
			ok:   true,
			want: "pollington v0.0.0-20261022203949-847108053d73+dirty",
		},
		{
			name: "empty version falls back",
			info: &debug.BuildInfo{},
			ok:   true,
			want: "pollington (devel)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := versionString(tc.info, tc.ok); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
