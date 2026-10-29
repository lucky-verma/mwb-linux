.PHONY: build install uninstall clean test fmt lint check bump install-gui uninstall-gui test-gui deb

SYSTEMD_USER_DIR := $(HOME)/.config/systemd/user
GUI_SHARE_DIR := $(HOME)/.local/share/mwb-gui
GUI_BIN_DIR := $(HOME)/.local/bin
GUI_APPS_DIR := $(HOME)/.local/share/applications
GUI_DESKTOP := io.github.lucky_verma.MwbLinux.desktop
GUI_AUTOSTART_DIR := $(HOME)/.config/autostart
GUI_TRAY_DESKTOP := io.github.lucky_verma.MwbLinux.Tray.desktop

build:
	go build -o mwb ./cmd/mwb

install: build
	install -D mwb $(HOME)/go/bin/mwb
	install -d $(SYSTEMD_USER_DIR)
	install -m 644 mwb.service $(SYSTEMD_USER_DIR)/mwb.service
	systemctl --user daemon-reload
	@echo ""
	@echo "Installed. To start:"
	@echo "  systemctl --user enable --now mwb"
	@echo ""
	@echo "View logs:"
	@echo "  journalctl --user -u mwb -f"

uninstall:
	systemctl --user disable --now mwb || true
	rm -f $(SYSTEMD_USER_DIR)/mwb.service
	rm -f $(HOME)/go/bin/mwb
	systemctl --user daemon-reload

# Settings window. Per-user like `make install`: no sudo. Needs
# python3-gi, python3-gi-cairo and gir1.2-gtk-4.0.
install-gui:
	install -d $(GUI_SHARE_DIR)/mwb_gui $(GUI_BIN_DIR) $(GUI_APPS_DIR) $(GUI_AUTOSTART_DIR)
	install -m 644 gui/mwb_gui/*.py $(GUI_SHARE_DIR)/mwb_gui/
	install -m 755 gui/mwb-gui $(GUI_BIN_DIR)/mwb-gui
	install -m 644 gui/$(GUI_DESKTOP) $(GUI_APPS_DIR)/$(GUI_DESKTOP)
	sed 's|^Exec=mwb-gui|Exec=$(GUI_BIN_DIR)/mwb-gui|' gui/$(GUI_TRAY_DESKTOP) > $(GUI_AUTOSTART_DIR)/$(GUI_TRAY_DESKTOP)
	@echo ""
	@echo "Installed mwb-gui. Open \"Mouse Without Borders\" from your apps, or run: mwb-gui"
	@echo "If it does not start: sudo apt install python3-gi python3-gi-cairo gir1.2-gtk-4.0"

uninstall-gui:
	rm -rf $(GUI_SHARE_DIR)
	rm -f $(GUI_BIN_DIR)/mwb-gui $(GUI_APPS_DIR)/$(GUI_DESKTOP) $(GUI_AUTOSTART_DIR)/$(GUI_TRAY_DESKTOP)

test-gui:
	python3 -m unittest discover -s gui/tests

# Build the release .deb (mwb + mwb-gui) into dist/ from .goreleaser.yaml,
# exactly as CI does, without publishing anything. Needs goreleaser.
deb:
	goreleaser release --snapshot --clean --skip=publish,announce,sign
	@ls -1 dist/*.deb

clean:
	rm -f mwb

test:
	go test ./...

fmt:
	gofmt -w .

lint:
	golangci-lint run

check: fmt lint test

bump: ## generate a new version with svu
	@$(MAKE) build
	@$(MAKE) test
	@$(MAKE) fmt
	$(MAKE) lint
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "Working directory is not clean. Please commit or stash changes before bumping version."; \
		exit 1; \
	fi
	@echo "Creating new tag..."
	@version=$$(svu next); \
		git tag -a $$version -m "Version $$version"; \
		echo "Tagged version $$version"; \
		echo "Pushing tag $$version to origin..."; \
		git push origin $$version

