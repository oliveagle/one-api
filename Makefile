# one-api Makefile

BINARY_NAME := one-api
INSTALL_DIR := $(HOME)/opt/one-api
BIN_DIR := $(INSTALL_DIR)/bin
LOG_DIR := $(INSTALL_DIR)/logs

BUILD_DIR := bin
GO := go
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build install restart clean

all: build install restart

build:
	@echo "==> Building $(BINARY_NAME)..."
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

install: build
	@echo "==> Installing to $(INSTALL_DIR)..."
	@mkdir -p $(BIN_DIR) $(LOG_DIR)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(BIN_DIR)/
	@echo "==> Installed $(BIN_DIR)/$(BINARY_NAME)"

restart: install
	@echo "==> Restarting one-api via supervisord..."
	@sudo supervisorctl restart one_api 2>/dev/null || sudo supervisorctl start one_api

stop:
	@sudo supervisorctl stop one_api

status:
	@sudo supervisorctl status one_api

clean:
	rm -rf $(BUILD_DIR)

# 交叉编译
build-linux:
	@echo "==> Building for Linux..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux .
