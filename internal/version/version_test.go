package version

import "testing"

func TestDefaultIsDev(t *testing.T) {
	t.Parallel()
	if Version != "dev" {
		t.Errorf("Version = %q without ldflags, want %q", Version, "dev")
	}
}

func TestShort(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty is dev", raw: "", want: "dev"},
		{name: "spaces only is dev", raw: "  ", want: "dev"},
		{name: "dev as is", raw: "dev", want: "dev"},
		{name: "plain semver", raw: "4.0.0", want: "4.0.0"},
		{name: "tag with v", raw: "v4.0.0", want: "4.0.0"},
		{name: "surrounding spaces", raw: " 4.0.0\n", want: "4.0.0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Short(tc.raw); got != tc.want {
				t.Errorf("Short(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
