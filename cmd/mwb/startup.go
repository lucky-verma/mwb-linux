// cmd/mwb/startup.go
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/config"
	"github.com/lucky-verma/mwb-linux/internal/status"
)

const startupRetry = 2 * time.Second

// waitForConfig keeps mwb running while it has no usable config: it says why
// in the status file and tries again whenever the file changes. Saving the
// settings then starts mwb with no restart, and a fresh install does not
// crash-loop the service. It returns once the config loads.
func waitForConfig(path string, firstErr error) *config.Config {
	st := status.NewWriter(status.Path(), status.Status{PID: os.Getpid(), Version: version})
	report := func(err error) {
		if errors.Is(err, fs.ErrNotExist) {
			slog.Info("not set up yet; waiting for settings", "config", path,
				"hint", "open Mouse Without Borders (mwb-gui), or create the file with host, key and name")
			_ = st.Waiting(status.StateNotConfigured, "")
			return
		}
		slog.Error("settings cannot be used; waiting for them to be fixed", "config", path, "err", err)
		_ = st.Waiting(status.StateConfigError, err.Error())
	}
	report(firstErr)
	last := modTime(path)
	for {
		time.Sleep(startupRetry)
		m := modTime(path)
		if m.Equal(last) {
			continue
		}
		last = m
		cfg, err := config.Load(path)
		if err == nil {
			slog.Info("settings found; starting")
			return cfg
		}
		report(err)
	}
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// waitForInterface returns the address mwb uses on the named interface,
// waiting while it has none (Wi-Fi still connecting, cable unplugged).
func waitForInterface(name string, st *status.Writer) net.IP {
	reported := false
	for {
		ip, err := interfaceIP(name)
		if err == nil {
			slog.Info("using one network interface only", "interface", name, "address", ip)
			return ip
		}
		if !reported {
			slog.Warn("waiting for the chosen network interface", "interface", name, "err", err)
			_ = st.Waiting(status.StateNoNetwork, err.Error())
			reported = true
		}
		time.Sleep(startupRetry)
	}
}

// interfaceIP picks the interface's address another machine would use: a
// private IPv4 address first, then any IPv4, then a global IPv6 address.
func interfaceIP(name string) (net.IP, error) {
	ifc, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("no network interface named %q", name)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return nil, err
	}
	var ips []net.IP
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok {
			ips = append(ips, ipn.IP)
		}
	}
	if ip := pickInterfaceIP(ips); ip != nil {
		return ip, nil
	}
	return nil, fmt.Errorf("%s has no usable address (not connected?)", name)
}

func pickInterfaceIP(ips []net.IP) net.IP {
	for _, want := range []func(net.IP) bool{
		func(ip net.IP) bool { return ip.To4() != nil && ip.IsPrivate() },
		func(ip net.IP) bool { return ip.To4() != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() },
		func(ip net.IP) bool { return ip.To4() == nil && ip.IsGlobalUnicast() },
	} {
		for _, ip := range ips {
			if want(ip) {
				return ip
			}
		}
	}
	return nil
}
