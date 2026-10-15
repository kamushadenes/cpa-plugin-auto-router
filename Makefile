BIN_DIR := $(CURDIR)/bin
PLUGIN := $(BIN_DIR)/auto-router.so
# Installed file name carries the plugin version: the host only swaps a loaded plugin when the
# selected file path changes, so an in-place overwrite of a fixed name keeps the old code running.
VERSION := $(shell grep -m1 -E '^[[:space:]]+Version:[[:space:]]+"' plugin.go | sed -E 's/.*"(.*)".*/\1/')
GIT_SHA := $(shell git rev-parse --short HEAD)
PLUGIN_FILE := auto-router-v$(VERSION).so
# Production proxy runs in LXC 139 (see docs/runbook-production.md); ~/cliproxyapi is retired.
PROD_HOST := root@10.23.23.12
PROD_CONFIG := /opt/cliproxy/config.yaml
INSTALL_DIR := /opt/cliproxy/plugins
TEST_INSTALL_DIR := /home/hermes/cliproxyapi-test/plugins

.PHONY: build test install clean
build: $(PLUGIN)
$(PLUGIN): $(shell find . -name '*.go' -not -path './updater/*') go.mod
	mkdir -p $(BIN_DIR)
	go build -buildmode=c-shared -o $(PLUGIN) .
	rm -f $(BIN_DIR)/auto-router.h
test:
	go test ./...
	cd updater && uv run --with pyyaml --with pyarrow --with pytest --python 3.12 python -m pytest -q
# The plugin set is only re-evaluated on a config reload, and the watcher reloads only on a config
# content change: append a deploy marker (the existing convention at the end of config.yaml).
install: build
	test -n "$(VERSION)"
	scp -q $(PLUGIN) $(PROD_HOST):/tmp/$(PLUGIN_FILE)
	ssh $(PROD_HOST) 'rm -f $(INSTALL_DIR)/auto-router*.so && install -m 0644 -o cliproxy -g cliproxy /tmp/$(PLUGIN_FILE) $(INSTALL_DIR)/$(PLUGIN_FILE) && rm /tmp/$(PLUGIN_FILE)'
	ssh $(PROD_HOST) 'printf "# auto-router deploy %s v%s\n" "$(GIT_SHA)" "$(VERSION)" >> $(PROD_CONFIG)'
# TEST is restarted manually (see README), so it needs no config-content change to pick the file up.
install-test: build
	test -n "$(VERSION)"
	mkdir -p $(TEST_INSTALL_DIR)
	rm -f $(TEST_INSTALL_DIR)/auto-router*.so
	install -m 0644 $(PLUGIN) $(TEST_INSTALL_DIR)/$(PLUGIN_FILE)
clean:
	rm -rf $(BIN_DIR)
