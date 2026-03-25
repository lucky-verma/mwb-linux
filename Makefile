.PHONY: build install uninstall clean test fmt lint check bump

SYSTEMD_USER_DIR := $(HOME)/.config/systemd/user

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

