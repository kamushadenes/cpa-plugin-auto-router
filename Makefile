BIN_DIR := $(CURDIR)/bin
PLUGIN := $(BIN_DIR)/auto-router.so
# Production proxy runs in LXC 139 (see docs/runbook-production.md); ~/cliproxyapi is retired.
PROD_HOST := root@10.23.23.12
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
install: build
	scp -q $(PLUGIN) $(PROD_HOST):/tmp/auto-router.so
	ssh $(PROD_HOST) 'rm -f $(INSTALL_DIR)/auto-router*.so && install -m 0644 -o cliproxy -g cliproxy /tmp/auto-router.so $(INSTALL_DIR)/auto-router.so && rm /tmp/auto-router.so'
install-test: build
	mkdir -p $(TEST_INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(TEST_INSTALL_DIR)/auto-router.so
clean:
	rm -rf $(BIN_DIR)
