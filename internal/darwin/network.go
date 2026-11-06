//go:build darwin

package darwin

import (
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"

	"github.com/egoist/mygo/internal/darwin/mainthread"
	"github.com/egoist/mygo/internal/platform"
)

// Network.framework, a C API: no Objective-C objects, no reference
// counting beyond the monitor itself, and blocks instead of a delegate.
// Its functions run on the main thread (mainthread.Forward); libdispatch
// runs on the calling thread.
var (
	nwPathFns           [7]uintptr
	dispatchQueueCreate func(label *byte, attr uintptr) uintptr
)

// The functions of Network.framework, in nwPathFns.
const (
	nwMonitorCreate = iota
	nwMonitorSetUpdateHandler
	nwMonitorSetQueue
	nwMonitorStart
	nwGetStatus
	nwIsExpensive
	nwIsConstrained
)

func nwPathMonitorCreate() uintptr { return mainthread.Forward[uintptr](nwPathFns[nwMonitorCreate]) }

func nwPathMonitorSetUpdateHandler(monitor, handler uintptr) {
	mainthread.Forward[struct{}](nwPathFns[nwMonitorSetUpdateHandler], monitor, handler)
}

func nwPathMonitorSetQueue(monitor, queue uintptr) {
	mainthread.Forward[struct{}](nwPathFns[nwMonitorSetQueue], monitor, queue)
}

func nwPathMonitorStart(monitor uintptr) {
	mainthread.Forward[struct{}](nwPathFns[nwMonitorStart], monitor)
}

func nwPathGetStatus(path uintptr) int32 {
	return mainthread.Forward[int32](nwPathFns[nwGetStatus], path)
}

func nwPathIsExpensive(path uintptr) bool {
	return mainthread.Forward[bool](nwPathFns[nwIsExpensive], path)
}

func nwPathIsConstrained(path uintptr) bool {
	return mainthread.Forward[bool](nwPathFns[nwIsConstrained], path)
}

// nw_path_status_satisfied: the path has a usable route to send and
// receive data on.
const nwPathStatusSatisfied = 1

// networkMonitor is the app's single path monitor, started by the first
// Status or Watch. Its handler runs on a queue of its own, so that Status
// can wait on the main thread for the first path, which the monitor
// reports asynchronously, as Chromium's NetworkChangeNotifierApple waits
// for its first connection type.
var networkMonitor struct {
	once  sync.Once
	ready chan struct{} // closed with the first path

	mu     sync.Mutex
	status platform.NetworkStatus

	watching bool // on the main thread
}

type network struct{ b *Backend }

func (b *Backend) Network() platform.Network { return network{b} }

// Status returns the status of the monitor's latest path.
func (network) Status() platform.NetworkStatus {
	startNetworkMonitor()
	select {
	case <-networkMonitor.ready:
	case <-time.After(time.Second):
		// The first path comes within milliseconds; never hang the app
		// on a monitor that does not answer.
	}
	networkMonitor.mu.Lock()
	defer networkMonitor.mu.Unlock()
	return networkMonitor.status
}

// Watch reports the paths after the first to AppHandler.NetworkChanged.
func (network) Watch() {
	networkMonitor.watching = true
	startNetworkMonitor()
}

// startNetworkMonitor creates and starts the monitor once. Network.framework
// is loaded only then, by apps that ask.
func startNetworkMonitor() {
	networkMonitor.once.Do(func() {
		networkMonitor.ready = make(chan struct{})
		lib := mustDlopen("/System/Library/Frameworks/Network.framework/Network")
		for i, name := range []string{
			"nw_path_monitor_create", "nw_path_monitor_set_update_handler", "nw_path_monitor_set_queue",
			"nw_path_monitor_start", "nw_path_get_status", "nw_path_is_expensive", "nw_path_is_constrained",
		} {
			nwPathFns[i] = mustDlsym(lib, name)
		}
		mainthread.Bind(&dispatchQueueCreate, purego.RTLD_DEFAULT, "dispatch_queue_create")

		m := nwPathMonitorCreate()
		if m == 0 {
			close(networkMonitor.ready)
			return
		}
		first := true
		// The handler runs on the monitor's serial queue, never on the main
		// thread, and touches no Objective-C object.
		blk := newBlock(func(_ objc.Block, path uintptr) {
			s := platform.NetworkStatus{
				Online:  nwPathGetStatus(path) == nwPathStatusSatisfied,
				Metered: nwPathIsExpensive(path) || nwPathIsConstrained(path),
			}
			networkMonitor.mu.Lock()
			networkMonitor.status = s
			networkMonitor.mu.Unlock()
			if first {
				first = false
				close(networkMonitor.ready)
				return
			}
			theBackend.post(func() {
				if networkMonitor.watching {
					theBackend.h.NetworkChanged(s)
				}
			})
		})
		// The monitor copies the handler.
		nwPathMonitorSetUpdateHandler(m, uintptr(blk))
		blockRelease(uintptr(blk))
		label := []byte("mygo.network\x00")
		nwPathMonitorSetQueue(m, dispatchQueueCreate(&label[0], 0))
		// The monitor lives as long as the app.
		nwPathMonitorStart(m)
	})
}
