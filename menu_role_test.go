//go:build !js

package mygo

import (
	"testing"

	"github.com/egoist/mygo/internal/platform"
)

// The system fills the services menu, so the role is a submenu without items, in the default application menu too.
func TestTheServicesRoleIsASubmenuWithoutItems(t *testing.T) {
	snap := NewMenu([]*MenuItem{{Role: RoleAppMenu}, {Role: RoleServices}}).snapshot()
	check := func(where string, item *platform.MenuItem) {
		t.Helper()
		if item.Type != platform.MenuItemSubmenu || item.Submenu == nil || len(item.Submenu.Items) != 0 {
			t.Fatalf("%s: type %v, submenu %+v", where, item.Type, item.Submenu)
		}
	}
	check("the role alone", snap.Items[1])
	check("the application menu", snap.Items[0].Submenu.Items[2])
}
