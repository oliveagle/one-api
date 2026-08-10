# one-api Makefile

BINARY_NAME := one-api
INSTALL_DIR := $(HOME)/opt/one-api
BIN_DIR := $(INSTALL_DIR)/bin
LOG_DIR := $(INSTALL_DIR)/logs
RUN_USER  ?= $(shell id -un)
RUN_GROUP ?= $(shell id -gn)

BUILD_DIR := bin
GO := go
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build install restart clean

all: build install restart

build:
	@echo "==> Building $(BINARY_NAME)..."
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) .

# 用 sudo cp 后 / 一旦以 root 写入 .db，重启的服务会 "attempt to write a readonly database" 而 FATAL。
# install 结束后强制 chown，使整个 install dir 与未来 db/wal/shm 都归运行时用户所有。
install: build
	@echo "==> Installing to $(INSTALL_DIR) (ownership $(RUN_USER):$(RUN_GROUP))..."
	@mkdir -p $(BIN_DIR) $(LOG_DIR)
	cp $(BUILD_DIR)/$(BINARY_NAME) $(BIN_DIR)/
	@sudo chown -R $(RUN_USER):$(RUN_GROUP) $(INSTALL_DIR)
	@echo "==> Installed $(BIN_DIR)/$(BINARY_NAME) ($(INSTALL_DIR) owned by $(RUN_USER):$(RUN_GROUP))"

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
