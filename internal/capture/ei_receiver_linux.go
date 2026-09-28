//go:build linux && wayland_portal && cgo

package capture

/*
#cgo pkg-config: libei-1.0
#include <errno.h>
#include <poll.h>
#include <libei.h>

static void mwb_bind_receiver_caps(struct ei_seat *seat) {
	ei_seat_bind_capabilities(seat,
		EI_DEVICE_CAP_POINTER,
		EI_DEVICE_CAP_POINTER_ABSOLUTE,
		EI_DEVICE_CAP_KEYBOARD,
		EI_DEVICE_CAP_BUTTON,
		EI_DEVICE_CAP_SCROLL,
		NULL);
}

static void mwb_configure_receiver(struct ei *ei) {
	ei_configure_name(ei, "mwb-linux");
}

static int mwb_ei_poll(struct ei *ei, int timeout_ms) {
	struct pollfd pfd = {
		.fd = ei_get_fd(ei),
		.events = POLLIN,
	};
	int rc;
	do {
		rc = poll(&pfd, 1, timeout_ms);
	} while (rc < 0 && errno == EINTR);
	return rc < 0 ? -errno : rc;
}

static int mwb_button_is_press(struct ei_event *event) {
	return ei_event_button_get_is_press(event) ? 1 : 0;
}

static int mwb_key_is_press(struct ei_event *event) {
	return ei_event_keyboard_get_key_is_press(event) ? 1 : 0;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"math"
	"syscall"
)

const logicalPixelsPerWheelStep = 10.0

type axisAccumulator struct {
	x float64
	y float64
}

func (a *axisAccumulator) take(x, y float64) (int32, int32) {
	return takeAxis(&a.x, x), takeAxis(&a.y, y)
}

func takeAxis(remainder *float64, delta float64) int32 {
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return 0
	}
	total := *remainder + delta
	output := truncInt32(total)
	if total > math.MaxInt32 || total < math.MinInt32 {
		*remainder = 0
	} else {
		*remainder = total - float64(output)
	}
	return output
}

func truncInt32(value float64) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	return int32(value)
}

type eiReceiver struct {
	context *C.struct_ei
	motion  axisAccumulator
	scroll  axisAccumulator
}

func (r *eiReceiver) continuousScroll(dx, dy float64) (int32, int32) {
	wheelUnitsPerPixel := 120.0 / logicalPixelsPerWheelStep
	return r.scroll.take(-dx*wheelUnitsPerPixel, -dy*wheelUnitsPerPixel)
}

func (r *eiReceiver) discard() {
	if r != nil && r.context != nil {
		C.ei_unref(r.context)
		r.context = nil
	}
}

func newEIReceiver(fd int) (*eiReceiver, error) {
	if fd < 0 {
		return nil, errors.New("invalid EIS file descriptor")
	}
	ei := C.ei_new_receiver(nil)
	if ei == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("create libei receiver")
	}
	C.mwb_configure_receiver(ei)
	if rc := int(C.ei_setup_backend_fd(ei, C.int(fd))); rc < 0 {
		C.ei_unref(ei)
		return nil, fmt.Errorf("connect libei receiver: %w", syscall.Errno(-rc))
	}
	return &eiReceiver{context: ei}, nil
}

func (r *eiReceiver) Run(ctx context.Context, owner *Capturer) error {
	if r == nil || r.context == nil {
		return errors.New("libei receiver is not initialized")
	}
	defer func() {
		C.ei_unref(r.context)
		r.context = nil
	}()

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		ready := int(C.mwb_ei_poll(r.context, 250))
		if ready < 0 {
			return fmt.Errorf("poll libei receiver: %w", syscall.Errno(-ready))
		}
		if ready == 0 {
			continue
		}
		C.ei_dispatch(r.context)
		for {
			event := C.ei_get_event(r.context)
			if event == nil {
				break
			}
			err := r.handleEvent(event, owner)
			C.ei_event_unref(event)
			if err != nil {
				return err
			}
		}
	}
}

func (r *eiReceiver) handleEvent(event *C.struct_ei_event, owner *Capturer) error {
	switch C.ei_event_get_type(event) {
	case C.EI_EVENT_DISCONNECT:
		return errors.New("libei receiver disconnected")
	case C.EI_EVENT_SEAT_ADDED:
		if seat := C.ei_event_get_seat(event); seat != nil {
			C.mwb_bind_receiver_caps(seat)
		}
	case C.EI_EVENT_POINTER_MOTION:
		dx, dy := r.motion.take(
			float64(C.ei_event_pointer_get_dx(event)),
			float64(C.ei_event_pointer_get_dy(event)),
		)
		owner.handleNativeMotion(dx, dy)
	case C.EI_EVENT_BUTTON_BUTTON:
		owner.handleNativeButton(uint32(C.ei_event_button_get_button(event)), C.mwb_button_is_press(event) != 0)
	case C.EI_EVENT_KEYBOARD_KEY:
		owner.handleNativeKey(uint32(C.ei_event_keyboard_get_key(event)), C.mwb_key_is_press(event) != 0)
	case C.EI_EVENT_SCROLL_DISCRETE:
		owner.handleNativeScroll(
			-int32(C.ei_event_scroll_get_discrete_dx(event)),
			-int32(C.ei_event_scroll_get_discrete_dy(event)),
		)
	case C.EI_EVENT_SCROLL_DELTA:
		x, y := r.continuousScroll(
			float64(C.ei_event_scroll_get_dx(event)),
			float64(C.ei_event_scroll_get_dy(event)),
		)
		owner.handleNativeScroll(x, y)
	}
	return nil
}
