//go:build darwin

package e2e

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/internal/darwin"
	"github.com/egoist/mygo/ui"
)

// AppKit fills only the first menu it is given, so each application menu
// holds that one menu in the item of the services role.
func TestTheServicesRoleHoldsTheServicesMenuOfTheApp(t *testing.T) {
	newWindow(t, mygo.WindowOptions{Title: "Services", Width: 200, Height: 100, Content: ui.View(func(c *ui.Context) {})})
	defer mygo.App.SetMenu(nil)
	for _, title := range []string{"First", "Second"} {
		mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{{Label: title, Submenu: []*mygo.MenuItem{
			{Label: "Services", Role: mygo.RoleServices},
		}}}))
		var services bool
		var err error
		mygo.RunOnMain(func() { services, err = darwin.TestIsServicesMenu(title, "Services") })
		if err != nil {
			t.Fatal(err)
		}
		if !services {
			t.Fatalf("the services item of %s does not hold the services menu of the app", title)
		}
	}
}
