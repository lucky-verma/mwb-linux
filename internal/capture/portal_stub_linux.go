//go:build linux && (!wayland_portal || !cgo)

package capture

func portalBackendBuilt() bool { return false }

func newPortalCapture(*Capturer) (nativeCapture, error) {
	return nil, errPortalCaptureUnavailable
}
