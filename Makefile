PKG := github.com/lightninglabs/loop

GOTEST := GO111MODULE=on go test -v

GOFILES_NOVENDOR = $(shell find . -type f -name '*.go' -not -path "./vendor/*")
MOBILE_PKG := $(PKG)/mobile
GO_BIN := ${GOPATH}/bin
GOMOBILE_BIN := GO111MODULE=off $(GO_BIN)/gomobile
MOBILE_BUILD_DIR :=${GOPATH}/src/$(MOBILE_PKG)/build
IOS_BUILD_DIR := $(MOBILE_BUILD_DIR)/ios
IOS_BUILD := $(IOS_BUILD_DIR)/Loopmobile.framework
ANDROID_BUILD_DIR := $(MOBILE_BUILD_DIR)/android
ANDROID_BUILD := $(ANDROID_BUILD_DIR)/Loopmobile.aar

GOLIST := go list $(PKG)/... | grep -v '/vendor/'

LINT_BIN := $(GO_BIN)/golangci-lint
LINT_PKG := github.com/golangci/golangci-lint/cmd/golangci-lint
LINT_COMMIT := v1.18.0
LINT = $(LINT_BIN) run -v

DEPGET := cd /tmp && GO111MODULE=on go get -v
XARGS := xargs -L 1

TEST_FLAGS = -test.timeout=20m

UNIT := $(GOLIST) | $(XARGS) env $(GOTEST) $(TEST_FLAGS)

$(LINT_BIN):
	@$(call print, "Fetching linter")
	$(DEPGET) $(LINT_PKG)@$(LINT_COMMIT)

unit: 
	@$(call print, "Running unit tests.")
	$(UNIT)

fmt:
	@$(call print, "Formatting source.")
	gofmt -l -w -s $(GOFILES_NOVENDOR)

lint: $(LINT_BIN)
	@$(call print, "Linting source.")
	$(LINT)

mobile-rpc:
	@$(call print, "Creating mobile RPC from protos.")
	cd ./mobile; ./gen_bindings.sh

vendor:
	@$(call print, "Re-creating vendor directory.")
	rm -r vendor/; GO111MODULE=on go mod vendor

ios: vendor mobile-rpc
	@$(call print, "Building iOS framework ($(IOS_BUILD)).")
	mkdir -p $(IOS_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=ios -tags="ios $(DEV_TAGS) autopilotrpc experimental" $(LDFLAGS) -v -o $(IOS_BUILD) $(MOBILE_PKG)

android: vendor mobile-rpc
	@$(call print, "Building Android library ($(ANDROID_BUILD)).")
	mkdir -p $(ANDROID_BUILD_DIR)
	$(GOMOBILE_BIN) bind -target=android -tags="android $(DEV_TAGS) autopilotrpc experimental" $(LDFLAGS) -v -o $(ANDROID_BUILD) $(MOBILE_PKG)

mobile: ios android

.PHONY: unit \
	mobile-rpc \
	vendor \
	ios \
	android \
	mobile \
