# go-xiaozhi Makefile
# 一键构建、打包可移植部署包（含 sherpa-onnx 动态库）、运行、清理。
#
# 常用目标：
#   make            构建二进制（默认）
#   make build      同上
#   make dist       打包部署包到 dist/，并生成 .tar.gz
#   make run        本地运行（读取 conf/biz.yaml）
#   make libs       仅拷出 sherpa 动态库到 dist/lib
#   make clean      清理构建产物
#   make check      检查构建产物依赖的动态库是否齐全
#
# 可覆盖变量：
#   make dist VERSION=v1.0.0 BINARY_NAME=xiaozhi

SHELL := /bin/bash

# ---- 可配置变量 ----
BINARY_NAME  ?= go-xiaozhi
VERSION      ?= dev
CMD_PKG      ?= ./cmd
DIST_DIR     ?= dist
BUILD_DIR    ?= bin
GO           ?= go
# rpath 让二进制从自身旁边的 lib/ 目录加载 sherpa .so，便于整体拷贝部署。
# 单引号阻止 shell/make 展开 $ORIGIN，保证字面量写入二进制 RUNPATH。
RPATH_LDFLAGS = '-Wl,-rpath,$$ORIGIN/lib'

# ---- sherpa 动态库定位（从 Go module cache 中找）----
SHERPA_MOD   := $(shell $(GO) list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-linux 2>/dev/null)
SHERPA_LIBDIR ?= $(SHERPA_MOD)/lib/x86_64-unknown-linux-gnu

# 默认目标
.PHONY: all build dist libs run check clean help
all: build

##@ 构建

# 构建带 rpath 的可移植二进制到 bin/
build: bin/$(BINARY_NAME)  ## 构建二进制（带 rpath，可移植）

bin/$(BINARY_NAME): $(wildcard cmd/**/*.go) $(wildcard **/*.go) go.mod go.sum
	@mkdir -p $(BUILD_DIR)
	@echo ">> building $(BINARY_NAME) (CGO + rpath \$\$ORIGIN/lib)"
	CGO_ENABLED=1 CGO_LDFLAGS=$(RPATH_LDFLAGS) $(GO) build -trimpath -o $@ $(CMD_PKG)
	@echo ">> built $@"

##@ 打包

# 拷出 sherpa 动态库（若未启用 sherpa 依赖则跳过）
libs: ## 拷贝 sherpa 动态库到 dist/lib
	@mkdir -p $(DIST_DIR)/lib
	@if [ -z "$(SHERPA_MOD)" ]; then \
		echo ">> 注意: 未找到 sherpa-onnx-go-linux 模块，跳过动态库拷贝（VAD/ASR 将回退）"; \
	else \
		echo ">> 拷贝 sherpa 动态库: $(SHERPA_LIBDIR) -> $(DIST_DIR)/lib"; \
		cp -f $(SHERPA_LIBDIR)/*.so $(DIST_DIR)/lib/; \
		ls -1 $(DIST_DIR)/lib/; \
	fi

# 打包完整部署包：二进制 + 动态库 + 配置示例 + 文档
dist: build libs  ## 打包部署包到 dist/ 并生成 .tar.gz
	@echo ">> 组装部署包 $(DIST_DIR)/"
	@mkdir -p $(DIST_DIR)/conf
	cp -f bin/$(BINARY_NAME) $(DIST_DIR)/$(BINARY_NAME)
	cp -f conf/biz.yaml.example $(DIST_DIR)/conf/biz.yaml.example
	@[ -f README.md ] && cp -f README.md $(DIST_DIR)/README.md || true
	@[ -f LICENSE ] && cp -f LICENSE $(DIST_DIR)/LICENSE || true
	@# 生成模型下载说明（不打包模型本体，体积大）
	@echo ">> 生成 $(DIST_DIR)/MODELS.md"
	@printf '# 运行所需模型\n\n将模型放到运行目录的 models/ 下：\n\n## VAD (Silero)\n```bash\nmkdir -p models\nwget -O models/silero_vad.onnx https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/silero_vad.onnx\n```\n\n## ASR (SenseVoice int8)\n```bash\nwget https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2\ntar xf sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2 -C models\nrm sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2\n```\n\n## 运行\n```bash\ncp conf/biz.yaml.example conf/biz.yaml  # 填入密钥\n./$(BINARY_NAME) --config_path=conf/biz.yaml\n```\n' > $(DIST_DIR)/MODELS.md
	@# 打 tar.gz
	@echo ">> 生成 $(DIST_DIR)/$(BINARY_NAME)-$(VERSION).tar.gz"
	@tar -czf $(DIST_DIR)/$(BINARY_NAME)-$(VERSION).tar.gz -C $(DIST_DIR) \
		$(BINARY_NAME) lib conf README.md LICENSE MODELS.md 2>/dev/null || \
	 tar -czf $(DIST_DIR)/$(BINARY_NAME)-$(VERSION).tar.gz -C $(DIST_DIR) \
		$(BINARY_NAME) lib conf MODELS.md
	@echo ">> 完成。部署包内容："
	@ls -lh $(DIST_DIR)/

##@ 运行 / 检查

# 本地运行（默认读 conf/biz.yaml）
run: build  ## 本地运行二进制
	@echo ">> 运行 bin/$(BINARY_NAME)"
	./bin/$(BINARY_NAME)

# 检查二进制依赖的动态库是否都能找到（无 "not found" 即可）
check: build  ## 检查动态库依赖完整性
	@echo ">> 检查 bin/$(BINARY_NAME) 动态库依赖"
	@ldd bin/$(BINARY_NAME) | grep -i "not found" && \
		{ echo "!! 缺少动态库（见上）"; exit 1; } || \
		echo ">> 动态库依赖完整"

##@ 清理

clean:  ## 清理构建产物
	@echo ">> 清理构建产物"
	rm -rf $(BUILD_DIR) $(DIST_DIR)
	@echo ">> 已清理 bin/ 和 dist/"

##@ 帮助

help: ## 显示帮助
	@printf "用法: make <target>\n\n目标:\n"
	@awk 'BEGIN {FS = ":.*##"} \
	/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next } \
	/^[a-zA-Z_.-]+:.*##/ { printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
