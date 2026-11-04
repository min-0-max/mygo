//go:build darwin

package e2e

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/ui"
)

// The services role is a submenu that AppKit fills, so the item is not dead.
func TestTheServicesRoleHoldsASubmenu(t *testing.T) {
	newWindow(t, mygo.WindowOptions{Title: "Services", Width: 200, Height: 100, Content: ui.View(func(c *ui.Context) {})})
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{{Label: "Test", Submenu: []*mygo.MenuItem{
		{Label: "Services", Role: mygo.RoleServices},
	}}}))
	defer mygo.App.SetMenu(nil)
	var has bool
	var err error
	mygo.RunOnMain(func() { has, err = darwin.TestMenuItemHasSubmenu("Test", "Services") })
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("the item of the services role holds no submenu")
	}
}
