BIN_DIR := $(CURDIR)/bin
PLUGIN := $(BIN_DIR)/auto-router.so
INSTALL_DIR := /home/hermes/cliproxyapi/plugins
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
	mkdir -p $(INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(INSTALL_DIR)/auto-router.so
install-test: build
	mkdir -p $(TEST_INSTALL_DIR)
	install -m 0644 $(PLUGIN) $(TEST_INSTALL_DIR)/auto-router.so
clean:
	rm -rf $(BIN_DIR)
