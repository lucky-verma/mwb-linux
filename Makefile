.PHONY: build install uninstall clean test fmt lint check deps

BINARY := mwb
PREFIX := /usr/local
GOFLAGS := -trimpath -ldflags="-s -w"
SYSTEMD_USER_DIR := $(HOME)/.config/systemd/user

build:
	go build $(GOFLAGS) -o $(BINARY) ./cmd/mwb/

install: build
	sudo install -m 755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	install -d $(SYSTEMD_USER_DIR)
	install -m 644 mwb.service $(SYSTEMD_USER_DIR)/mwb.service
	systemctl --user daemon-reload
	@echo ""
	@echo "Installed to $(PREFIX)/bin/$(BINARY)"
	@echo ""
	@echo "To start:  mwb -bidi -edge left"
	@echo "Autostart: systemctl --user enable --now mwb"
	@echo "Logs:      journalctl --user -u mwb -f"

uninstall:
	systemctl --user disable --now mwb 2>/dev/null || true
	sudo rm -f $(PREFIX)/bin/$(BINARY)
	rm -f $(SYSTEMD_USER_DIR)/mwb.service
	systemctl --user daemon-reload

clean:
	rm -f $(BINARY)
	go clean

test:
	go test ./...

fmt:
	gofmt -w .

lint:
	@which golangci-lint > /dev/null 2>&1 && golangci-lint run || echo "golangci-lint not installed, skipping"

check: fmt test build

deps:
	sudo apt install -y xdotool xinput xclip x11-xserver-utils

