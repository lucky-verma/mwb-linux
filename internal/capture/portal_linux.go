//go:build linux && wayland_portal && cgo

package capture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalBusName      = "org.freedesktop.portal.Desktop"
	portalObjectPath   = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalInputIface   = "org.freedesktop.portal.InputCapture"
	portalRequestIface = "org.freedesktop.portal.Request"
	portalSessionIface = "org.freedesktop.portal.Session"

	portalKeyboardCapability = uint32(1)
	portalPointerCapability  = uint32(2)
	portalRequiredCaps       = portalKeyboardCapability | portalPointerCapability

	portalRequestTimeout = 10 * time.Second
	portalPromptTimeout  = 2 * time.Minute
)

var portalRequestSerial atomic.Uint64

type portalCapture struct {
	owner    *Capturer
	conn     *dbus.Conn
	object   dbus.BusObject
	session  dbus.ObjectPath
	version  uint32
	receiver *eiReceiver

	ctx    context.Context
	cancel context.CancelFunc
	sigCh  chan *dbus.Signal
	wg     sync.WaitGroup

	mu             sync.Mutex
	screen         ScreenInfo
	barrierIDs     map[uint32]struct{}
	activationID   uint32
	haveActivation bool
	started        bool
	closing        bool
	failed         bool

	reconfigureMu sync.Mutex
	closeOnce     sync.Once
	closeErr      error
}

type portalPosition struct {
	X1 int32
	Y1 int32
	X2 int32
	Y2 int32
}

type portalCursorPosition struct {
	X float64
	Y float64
}

func portalBackendBuilt() bool { return true }

func newPortalCapture(owner *Capturer) (capture nativeCapture, err error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("%w: connect to session bus: %v", errPortalCaptureUnavailable, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &portalCapture{
		owner:  owner,
		conn:   conn,
		object: conn.Object(portalBusName, portalObjectPath),
		ctx:    ctx,
		cancel: cancel,
		sigCh:  make(chan *dbus.Signal, 16),
	}
	defer func() {
		if err != nil {
			p.closeResources()
		}
	}()

	if p.version, err = p.uintProperty("version"); err != nil {
		return nil, portalSetupError("read InputCapture portal version", err)
	}
	supported, err := p.uintProperty("SupportedCapabilities")
	if err != nil {
		return nil, fmt.Errorf("read InputCapture capabilities: %w", err)
	}
	if supported&portalRequiredCaps != portalRequiredCaps {
		return nil, fmt.Errorf("InputCapture portal lacks keyboard or pointer support (capabilities %#x)", supported)
	}

	if err = p.createSession(); err != nil {
		return nil, err
	}
	if err = p.subscribeSignals(); err != nil {
		return nil, err
	}
	if err = p.configureBarriers(); err != nil {
		return nil, err
	}
	fd, err := p.connectToEIS()
	if err != nil {
		return nil, err
	}
	if p.receiver, err = newEIReceiver(fd); err != nil {
		return nil, err
	}
	return p, nil
}

func portalSetupError(action string, err error) error {
	var dbusErr *dbus.Error
	name := ""
	if errors.As(err, &dbusErr) {
		name = dbusErr.Name
	} else {
		var dbusValue dbus.Error
		if errors.As(err, &dbusValue) {
			name = dbusValue.Name
		}
	}
	if name != "" {
		switch name {
		case "org.freedesktop.DBus.Error.ServiceUnknown",
			"org.freedesktop.DBus.Error.NameHasNoOwner",
			"org.freedesktop.DBus.Error.UnknownInterface",
			"org.freedesktop.DBus.Error.UnknownMethod",
			"org.freedesktop.DBus.Error.UnknownObject":
			return fmt.Errorf("%w: %s: %v", errPortalCaptureUnavailable, action, err)
		}
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (p *portalCapture) Name() string { return "portal/libei" }

func (p *portalCapture) Screen() ScreenInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.screen
}

func (p *portalCapture) Start() error {
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return errors.New("portal capture is closing")
	}
	if p.started {
		p.mu.Unlock()
		return nil
	}
	p.started = true
	p.mu.Unlock()

	p.wg.Add(2)
	go func() {
		defer p.wg.Done()
		p.signalLoop()
	}()
	go func() {
		defer p.wg.Done()
		if err := p.receiver.Run(p.ctx, p.owner); err != nil && p.ctx.Err() == nil {
			p.fail(fmt.Errorf("libei input stream: %w", err))
		}
	}()

	if err := p.enable(); err != nil {
		p.cancel()
		p.wg.Wait()
		return fmt.Errorf("enable InputCapture portal: %w", err)
	}
	return nil
}

func (p *portalCapture) Reenter(x, y int32) error {
	p.mu.Lock()
	activationID := p.activationID
	haveActivation := p.haveActivation
	p.mu.Unlock()
	if !haveActivation {
		return nil
	}

	options := map[string]dbus.Variant{
		"activation_id":   dbus.MakeVariant(activationID),
		"cursor_position": dbus.MakeVariant(portalCursorPosition{X: float64(x), Y: float64(y)}),
	}
	if err := p.object.Call(portalInputIface+".Release", 0, p.session, options).Err; err != nil {
		err = fmt.Errorf("release portal capture: %w", err)
		p.fail(err)
		return err
	}
	p.mu.Lock()
	if p.haveActivation && p.activationID == activationID {
		p.haveActivation = false
	}
	p.mu.Unlock()
	return nil
}

func (p *portalCapture) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closing = true
		started := p.started
		p.mu.Unlock()

		if p.owner != nil {
			x, y := p.owner.SafeEntryPosition()
			if err := p.Reenter(x, y); err != nil {
				p.closeErr = err
			}
		}
		p.cancel()
		if started {
			p.wg.Wait()
		} else if p.receiver != nil {
			p.receiver.discard()
		}
		p.closeResources()
	})
	return p.closeErr
}

func (p *portalCapture) uintProperty(name string) (uint32, error) {
	var value dbus.Variant
	if err := p.object.Call("org.freedesktop.DBus.Properties.Get", 0, portalInputIface, name).Store(&value); err != nil {
		return 0, err
	}
	return decodePortalUint32(value)
}

func (p *portalCapture) createSession() error {
	if p.version >= 2 {
		return p.createSessionV2()
	}
	return p.createSessionV1()
}

func (p *portalCapture) createSessionV1() error {
	sessionToken := p.token("session")
	results, err := p.request("CreateSession", portalPromptTimeout, func(handleToken string) []interface{} {
		return []interface{}{"", map[string]dbus.Variant{
			"handle_token":         dbus.MakeVariant(handleToken),
			"session_handle_token": dbus.MakeVariant(sessionToken),
			"capabilities":         dbus.MakeVariant(portalRequiredCaps),
		}}
	})
	if err != nil {
		return fmt.Errorf("create InputCapture session: %w", err)
	}
	if err := p.storeSession(results); err != nil {
		return err
	}
	return requirePortalCaps(results)
}

func (p *portalCapture) createSessionV2() error {
	options := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(p.token("session")),
	}
	var results map[string]dbus.Variant
	if err := p.object.Call(portalInputIface+".CreateSession2", 0, options).Store(&results); err != nil {
		return fmt.Errorf("create InputCapture session: %w", err)
	}
	if err := p.storeSession(results); err != nil {
		return err
	}

	results, err := p.request("Start", portalPromptTimeout, func(handleToken string) []interface{} {
		startOptions := map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(handleToken),
			"capabilities": dbus.MakeVariant(portalRequiredCaps),
			"persist_mode": dbus.MakeVariant(uint32(2)),
		}
		if token := loadPortalRestoreToken(); token != "" {
			startOptions["restore_token"] = dbus.MakeVariant(token)
		}
		return []interface{}{p.session, "", startOptions}
	})
	if err != nil {
		return fmt.Errorf("start InputCapture session: %w", err)
	}
	if err := requirePortalCaps(results); err != nil {
		return err
	}
	if variant, ok := results["restore_token"]; ok {
		if token, ok := variant.Value().(string); ok && token != "" {
			savePortalRestoreToken(token)
		}
	}
	return nil
}

func (p *portalCapture) storeSession(results map[string]dbus.Variant) error {
	variant, ok := results["session_handle"]
	if !ok {
		return errors.New("portal response has no session handle")
	}
	session, ok := variant.Value().(dbus.ObjectPath)
	if !ok || !session.IsValid() {
		return fmt.Errorf("portal returned an invalid session handle %T", variant.Value())
	}
	p.session = session
	return nil
}

func requirePortalCaps(results map[string]dbus.Variant) error {
	variant, ok := results["capabilities"]
	if !ok {
		return errors.New("portal response has no capabilities")
	}
	capabilities, err := decodePortalUint32(variant)
	if err != nil {
		return fmt.Errorf("decode portal capabilities: %w", err)
	}
	if capabilities&portalRequiredCaps != portalRequiredCaps {
		return fmt.Errorf("portal session did not grant keyboard and pointer capture (capabilities %#x)", capabilities)
	}
	return nil
}

func (p *portalCapture) subscribeSignals() error {
	p.conn.Signal(p.sigCh)
	if err := p.conn.AddMatchSignal(
		dbus.WithMatchInterface(portalInputIface),
		dbus.WithMatchObjectPath(portalObjectPath),
	); err != nil {
		return fmt.Errorf("subscribe to InputCapture signals: %w", err)
	}
	if err := p.conn.AddMatchSignal(
		dbus.WithMatchInterface(portalSessionIface),
		dbus.WithMatchObjectPath(p.session),
	); err != nil {
		_ = p.conn.RemoveMatchSignal(
			dbus.WithMatchInterface(portalInputIface),
			dbus.WithMatchObjectPath(portalObjectPath),
		)
		return fmt.Errorf("subscribe to portal session close: %w", err)
	}
	return nil
}

func (p *portalCapture) connectToEIS() (int, error) {
	var fd dbus.UnixFD
	if err := p.object.Call(portalInputIface+".ConnectToEIS", 0, p.session, map[string]dbus.Variant{}).Store(&fd); err != nil {
		return -1, fmt.Errorf("connect portal to EIS: %w", err)
	}
	return int(fd), nil
}

func (p *portalCapture) configureBarriers() error {
	zones, zoneSet, err := p.getZones()
	if err != nil {
		return err
	}
	p.owner.mu.Lock()
	edge := p.owner.edgeSide
	p.owner.mu.Unlock()
	barriers, screen, err := barriersForEdge(zones, edge)
	if err != nil {
		return err
	}

	dbusBarriers := make([]map[string]dbus.Variant, 0, len(barriers))
	ids := make(map[uint32]struct{}, len(barriers))
	for _, barrier := range barriers {
		ids[barrier.ID] = struct{}{}
		dbusBarriers = append(dbusBarriers, map[string]dbus.Variant{
			"barrier_id": dbus.MakeVariant(barrier.ID),
			"position": dbus.MakeVariant(portalPosition{
				X1: barrier.Position.X1,
				Y1: barrier.Position.Y1,
				X2: barrier.Position.X2,
				Y2: barrier.Position.Y2,
			}),
		})
	}
	results, err := p.request("SetPointerBarriers", portalRequestTimeout, func(handleToken string) []interface{} {
		return []interface{}{p.session, map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(handleToken),
		}, dbusBarriers, zoneSet}
	})
	if err != nil {
		return fmt.Errorf("set portal pointer barriers: %w", err)
	}
	if failed := decodeUint32Slice(results["failed_barriers"]); len(failed) != 0 {
		return fmt.Errorf("portal rejected pointer barriers %v", failed)
	}

	p.mu.Lock()
	p.screen = screen
	p.barrierIDs = ids
	p.mu.Unlock()
	p.owner.setNativeScreen(screen)
	return nil
}

func (p *portalCapture) getZones() ([]portalZone, uint32, error) {
	results, err := p.request("GetZones", portalRequestTimeout, func(handleToken string) []interface{} {
		return []interface{}{p.session, map[string]dbus.Variant{
			"handle_token": dbus.MakeVariant(handleToken),
		}}
	})
	if err != nil {
		return nil, 0, fmt.Errorf("get portal zones: %w", err)
	}
	zoneSetVariant, ok := results["zone_set"]
	if !ok {
		return nil, 0, errors.New("portal zone response has no zone_set")
	}
	zoneSet, err := decodePortalUint32(zoneSetVariant)
	if err != nil {
		return nil, 0, fmt.Errorf("decode portal zone_set: %w", err)
	}
	zonesVariant, ok := results["zones"]
	if !ok {
		return nil, 0, errors.New("portal zone response has no zones")
	}
	zones, err := decodePortalZones(zonesVariant.Value())
	if err != nil {
		return nil, 0, fmt.Errorf("decode portal zones: %w", err)
	}
	return zones, zoneSet, nil
}

func (p *portalCapture) enable() error {
	return p.object.Call(portalInputIface+".Enable", 0, p.session, map[string]dbus.Variant{}).Err
}

func (p *portalCapture) signalLoop() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case signal := <-p.sigCh:
			if signal != nil {
				p.handleSignal(signal)
			}
		}
	}
}

func (p *portalCapture) handleSignal(signal *dbus.Signal) {
	if signal.Path == p.session && signal.Name == portalSessionIface+".Closed" {
		p.fail(errors.New("InputCapture portal closed the session"))
		return
	}
	if signal.Path != portalObjectPath || len(signal.Body) != 2 {
		return
	}
	session, ok := signal.Body[0].(dbus.ObjectPath)
	if !ok || session != p.session {
		return
	}
	options, ok := signal.Body[1].(map[string]dbus.Variant)
	if !ok {
		return
	}

	switch signal.Name {
	case portalInputIface + ".Activated":
		p.handleActivated(options)
	case portalInputIface + ".Deactivated":
		p.handleDeactivated(options)
	case portalInputIface + ".Disabled":
		p.clearActivation()
		p.regainLocal("portal capture disabled")
		p.reconfigureMu.Lock()
		err := p.enable()
		p.reconfigureMu.Unlock()
		if err != nil {
			p.fail(fmt.Errorf("re-enable InputCapture portal: %w", err))
		}
	case portalInputIface + ".ZonesChanged":
		p.regainLocal("portal zones changed")
		p.reconfigureMu.Lock()
		err := p.configureBarriers()
		if err == nil {
			err = p.enable()
		}
		p.reconfigureMu.Unlock()
		if err != nil {
			p.fail(fmt.Errorf("reconfigure InputCapture zones: %w", err))
		}
	}
}

func (p *portalCapture) handleActivated(options map[string]dbus.Variant) {
	idVariant, ok := options["activation_id"]
	if !ok {
		p.fail(errors.New("portal Activated signal has no activation_id"))
		return
	}
	id, err := decodePortalUint32(idVariant)
	if err != nil {
		p.fail(fmt.Errorf("decode portal activation_id: %w", err))
		return
	}

	p.mu.Lock()
	if p.haveActivation && p.activationID == id {
		p.mu.Unlock()
		return
	}
	p.activationID = id
	p.haveActivation = true
	barrierAllowed := true
	if variant, exists := options["barrier_id"]; exists {
		if barrierID, decodeErr := decodePortalUint32(variant); decodeErr != nil {
			barrierAllowed = false
		} else if barrierID != 0 {
			_, barrierAllowed = p.barrierIDs[barrierID]
		}
	}
	screen := p.screen
	p.mu.Unlock()

	if !barrierAllowed {
		x, y := p.owner.SafeEntryPosition()
		_ = p.Reenter(x, y)
		return
	}
	y := screen.Y + screen.Height/2
	if variant, exists := options["cursor_position"]; exists {
		if _, portalY, decodeErr := decodePortalPoint(variant.Value()); decodeErr == nil {
			y = truncInt32(portalY)
		}
	}
	if !p.owner.switchToRemote(y) {
		x, entryY := p.owner.SafeEntryPosition()
		_ = p.Reenter(x, entryY)
		p.regainLocal("portal activation did not match local ownership")
	}
}

func (p *portalCapture) handleDeactivated(options map[string]dbus.Variant) {
	p.mu.Lock()
	if variant, ok := options["activation_id"]; ok {
		id, err := decodePortalUint32(variant)
		if err == nil && p.haveActivation && id != p.activationID {
			p.mu.Unlock()
			return
		}
	}
	p.haveActivation = false
	p.mu.Unlock()
	p.regainLocal("portal capture deactivated")
}

func (p *portalCapture) clearActivation() {
	p.mu.Lock()
	p.haveActivation = false
	p.mu.Unlock()
}

func (p *portalCapture) regainLocal(reason string) {
	if !p.owner.IsActive() {
		slog.Warn(reason)
		p.owner.SetActive(true)
	}
}

func (p *portalCapture) fail(err error) {
	p.mu.Lock()
	closing := p.closing || p.failed
	p.failed = true
	p.mu.Unlock()
	if closing {
		return
	}
	slog.Error("native Wayland capture stopped", "err", err)
	if p.session != "" {
		_ = p.conn.Object(portalBusName, p.session).Call(portalSessionIface+".Close", 0).Err
	}
	p.clearActivation()
	p.regainLocal("native Wayland capture stopped")
	p.cancel()
}

func (p *portalCapture) request(method string, timeout time.Duration, args func(string) []interface{}) (map[string]dbus.Variant, error) {
	token := p.token("request")
	requestPath, err := p.requestPath(token)
	if err != nil {
		return nil, err
	}
	responseCh := make(chan *dbus.Signal, 1)
	p.conn.Signal(responseCh)
	defer p.conn.RemoveSignal(responseCh)
	match := []dbus.MatchOption{
		dbus.WithMatchInterface(portalRequestIface),
		dbus.WithMatchObjectPath(requestPath),
	}
	if err := p.conn.AddMatchSignal(match...); err != nil {
		return nil, fmt.Errorf("subscribe to %s response: %w", method, err)
	}
	defer func() { _ = p.conn.RemoveMatchSignal(match...) }()

	var returnedPath dbus.ObjectPath
	if err := p.object.Call(portalInputIface+"."+method, 0, args(token)...).Store(&returnedPath); err != nil {
		return nil, err
	}
	if returnedPath != requestPath {
		return nil, fmt.Errorf("portal returned unexpected request path %s", returnedPath)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case signal := <-responseCh:
		if signal == nil || len(signal.Body) != 2 {
			return nil, errors.New("portal returned a malformed response")
		}
		code, err := decodePortalUint32(signal.Body[0])
		if err != nil {
			return nil, fmt.Errorf("decode portal response: %w", err)
		}
		results, ok := signal.Body[1].(map[string]dbus.Variant)
		if !ok {
			return nil, fmt.Errorf("portal response has unexpected results type %T", signal.Body[1])
		}
		switch code {
		case 0:
			return results, nil
		case 1:
			return nil, errors.New("request was cancelled")
		default:
			return nil, fmt.Errorf("request failed with response code %d", code)
		}
	case <-timer.C:
		_ = p.conn.Object(portalBusName, requestPath).Call(portalRequestIface+".Close", 0).Err
		return nil, errors.New("timed out waiting for portal response")
	case <-p.ctx.Done():
		_ = p.conn.Object(portalBusName, requestPath).Call(portalRequestIface+".Close", 0).Err
		return nil, p.ctx.Err()
	}
}

func (p *portalCapture) token(kind string) string {
	return fmt.Sprintf("mwb_%s_%d", kind, portalRequestSerial.Add(1))
}

func (p *portalCapture) requestPath(token string) (dbus.ObjectPath, error) {
	names := p.conn.Names()
	if len(names) == 0 {
		return "", errors.New("session bus connection has no unique name")
	}
	sender := strings.TrimPrefix(names[0], ":")
	sender = strings.ReplaceAll(sender, ".", "_")
	path := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + sender + "/" + token)
	if !path.IsValid() {
		return "", errors.New("could not create a valid portal request path")
	}
	return path, nil
}

func (p *portalCapture) closeResources() {
	if p.conn == nil {
		return
	}
	p.conn.RemoveSignal(p.sigCh)
	_ = p.conn.RemoveMatchSignal(
		dbus.WithMatchInterface(portalInputIface),
		dbus.WithMatchObjectPath(portalObjectPath),
	)
	if p.session != "" {
		_ = p.conn.RemoveMatchSignal(
			dbus.WithMatchInterface(portalSessionIface),
			dbus.WithMatchObjectPath(p.session),
		)
		_ = p.conn.Object(portalBusName, p.session).Call(portalSessionIface+".Close", 0).Err
	}
	if err := p.conn.Close(); err != nil && p.closeErr == nil {
		p.closeErr = err
	}
	p.conn = nil
}

func decodePortalZones(value interface{}) ([]portalZone, error) {
	var zones []portalZone
	if err := dbus.Store([]interface{}{value}, &zones); err != nil {
		return nil, err
	}
	return zones, nil
}

func decodePortalPoint(value interface{}) (float64, float64, error) {
	var point portalCursorPosition
	if err := dbus.Store([]interface{}{value}, &point); err != nil {
		return 0, 0, err
	}
	return point.X, point.Y, nil
}

func decodeUint32Slice(variant dbus.Variant) []uint32 {
	if variant.Signature().Empty() {
		return nil
	}
	var values []uint32
	if err := dbus.Store([]interface{}{variant}, &values); err != nil {
		return nil
	}
	return values
}

func decodePortalUint32(value interface{}) (uint32, error) {
	var number uint32
	if err := dbus.Store([]interface{}{value}, &number); err != nil {
		return 0, err
	}
	return number, nil
}

func portalStatePath() string {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "mwb", "input-capture-token")
}

func loadPortalRestoreToken() string {
	path := portalStatePath()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func savePortalRestoreToken(token string) {
	path := portalStatePath()
	if path == "" {
		return
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		slog.Warn("save portal restore token", "err", err)
		return
	}
	_ = os.Chmod(directory, 0o700)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(token+"\n"), 0o600); err != nil {
		slog.Warn("save portal restore token", "err", err)
		return
	}
	_ = os.Chmod(temporary, 0o600)
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		slog.Warn("save portal restore token", "err", err)
	}
}
