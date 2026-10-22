package teleport_test

import (
	"context"
	"slices"
	"testing"

	"github.com/iamletenkov/stvor-agent/internal/teleport"
)

// TestExtensionName: the extension of all three cream calls is
// CreamTeleport.Extension — teleport-openwrt on OpenWrt (spec §4.3, R11);
// empty keeps teleport, the name of NewCream.
func TestExtensionName(t *testing.T) {
	t.Parallel()
	calls := []struct {
		name string
		sc   scenario
		call func(*teleport.CreamTeleport) error
		args func(ext string) []string
	}{
		{"managed-status", scenario{StdoutFile: "managed-status-joined.json"},
			func(c *teleport.CreamTeleport) error { _, err := c.Status(context.Background()); return err },
			func(ext string) []string { return []string{"run", ext, "managed-status"} }},
		{"set-managed", scenario{StdoutFile: "set-managed-applied.json"},
			func(c *teleport.CreamTeleport) error {
				_, err := c.SetManaged(context.Background(), teleport.Labels{Env: "prod", Space: "sp-1"})
				return err
			},
			func(ext string) []string {
				return []string{"run", "--lock-timeout", "20s", ext, "set-managed", "env=prod", "space=sp-1"}
			}},
		{"actions", scenario{StdoutFile: "cream/actions-teleport.json"},
			func(c *teleport.CreamTeleport) error { return c.CheckActions(context.Background()) },
			func(ext string) []string { return []string{"actions", ext, "--json"} }},
	}
	for _, ext := range []struct{ set, want string }{
		{"teleport-openwrt", "teleport-openwrt"},
		{"", "teleport"},
	} {
		for _, tc := range calls {
			t.Run(ext.want+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				c, dir := fake(t, tc.sc)
				c.Extension = ext.set
				if err := tc.call(c); err != nil {
					t.Fatalf("err = %v", err)
				}
				if got, want := readRecord(t, dir).Args, tc.args(ext.want); !slices.Equal(got, want) {
					t.Errorf("argv = %q, want %q", got, want)
				}
			})
		}
	}
	if got := teleport.NewCream("").Extension; got != teleport.DefaultExtension {
		t.Errorf("NewCream: Extension = %q, want %q", got, teleport.DefaultExtension)
	}
	if teleport.DefaultExtension != "teleport" {
		t.Errorf("DefaultExtension = %q", teleport.DefaultExtension)
	}
}
