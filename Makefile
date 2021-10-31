BUILD_DIR := build
PLATFORMS := darwin_amd64 linux_arm64
CROSS_TARGETS := $(PLATFORMS:%=$(BUILD_DIR)/%/flixproxy)
UPX_TARGETS := $(PLATFORMS:%=$(BUILD_DIR)/%/flixproxy.upx)

SRCS := $(shell find . -name "*.go")

.DEFAULT_GOAL := compile
.PHONY: clean test compile release upx

VERSION := $(shell git describe --tags --dirty)
EXTRA_LDFLAGS += -X \"main.VERSION=$(VERSION)\"

UPX_FLAGS ?= $(if $(V),,-q) -9 --brute

compile:
	go build -ldflags="$(EXTRA_LDFLAGS)" .

test:
	go vet ./...
	go test -cover $(if $(V),-v,) ./...

define CROSS_BUILD
GOOS=$(firstword $(subst _, ,$1)) \
GOARCH=$(word 2,$(subst _, ,$1)) \
go build -mod=readonly -trimpath -ldflags="-s -w $(EXTRA_LDFLAGS)" -o "$2" .
endef

$(BUILD_DIR)/.prepared:
	mkdir -p $(BUILD_DIR)
	touch $@

release: $(CROSS_TARGETS)
$(CROSS_TARGETS): $(BUILD_DIR)/.prepared $(SRCS)
	$(call CROSS_BUILD,$(word 2,$(subst /, ,$@)),$@)

upx: $(UPX_TARGETS)
$(UPX_TARGETS): $(BUILD_DIR)/%/flixproxy.upx : $(BUILD_DIR)/%/flixproxy
	rm -f $@
	upx $(UPX_FLAGS) $< -o $@

clean:
	rm -rf $(BUILD_DIR) flixproxy

$(V).SILENT:
