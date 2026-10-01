BIN_DIR := $(CURDIR)/bin
PLUGIN := $(BIN_DIR)/auto-router.so
# Production proxy runs in LXC 139 (see docs/runbook-production.md); ~/cliproxyapi is retired.
PROD_HOST := root@10.23.23.12
INSTALL_DIR := /opt/cliproxy/plugins
PROD_CONFIG := /opt/cliproxy/config.yaml
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
# The host reloads the plugin only on a config event that changes the .so path:
# name the file by content hash, then rewrite the config in place (cat > keeps
# the inode the fsnotify watcher is bound to) with a changed deploy marker.
install: build
	name=auto-router-$$(sha256sum $(PLUGIN) | cut -c1-12).so && \
	scp -q $(PLUGIN) $(PROD_HOST):/tmp/$$name && \
	ssh $(PROD_HOST) "umask 077; set -e; rm -f $(INSTALL_DIR)/auto-router*.so; install -m 0644 -o cliproxy -g cliproxy /tmp/$$name $(INSTALL_DIR)/$$name; rm /tmp/$$name; grep -v '^# auto-router deploy' $(PROD_CONFIG) > $(PROD_CONFIG).deploy-tmp; echo \"# auto-router deploy $$name\" >> $(PROD_CONFIG).deploy-tmp; cat $(PROD_CONFIG).deploy-tmp > $(PROD_CONFIG); rm $(PROD_CONFIG).deploy-tmp"
install-test: build
	mkdir -p $(TEST_INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(TEST_INSTALL_DIR)/auto-router.so
clean:
	rm -rf $(BIN_DIR)
