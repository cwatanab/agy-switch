.PHONY: build build-all test clean

BINARY_NAME=agy-switch
DIST_DIR=dist
VERSION=1.0.6
TAGS=netgo,osusergo
LDFLAGS=-s -w -buildid= -X 'github.com/ZhangNy301/agy-switch/pkg/cli.Version=$(VERSION)'
BUILD_FLAGS=-trimpath -tags="$(TAGS)" -ldflags="$(LDFLAGS)"

build:
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME) ./cmd/agy-switch

test:
	go test -v ./...

build-all: clean
	mkdir -p $(DIST_DIR)
	# Linux
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/agy-switch
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/agy-switch
	# macOS
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/agy-switch
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/agy-switch
	# Windows
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/agy-switch
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(BUILD_FLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-windows-arm64.exe ./cmd/agy-switch

clean:
	rm -rf $(DIST_DIR)
