.PHONY: build install clean tidy cross-build test

BINARY := dmr-plugin-feishu
INSTALL_DIR := $(HOME)/.dmr/plugins

build: tidy
	go build -o $(BINARY) ./cmd/dmr-plugin-feishu/

test:
	go test ./...

tidy:
	go mod tidy

cross-build: tidy
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY)-linux-amd64 ./cmd/dmr-plugin-feishu/
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o $(BINARY)-linux-arm64 ./cmd/dmr-plugin-feishu/
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY)-darwin-amd64 ./cmd/dmr-plugin-feishu/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o $(BINARY)-darwin-arm64 ./cmd/dmr-plugin-feishu/
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o $(BINARY)-windows-amd64.exe ./cmd/dmr-plugin-feishu/
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -o $(BINARY)-windows-arm64.exe ./cmd/dmr-plugin-feishu/

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/
	@echo "Installed to $(INSTALL_DIR)/$(BINARY)"

clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64 $(BINARY)-linux-arm64 $(BINARY)-darwin-amd64 $(BINARY)-darwin-arm64 $(BINARY)-windows-amd64.exe $(BINARY)-windows-arm64.exe

# Development helpers
dev: build
	./$(BINARY)

fmt:
	go fmt ./...

lint:
	golangci-lint run ./...

check: tidy fmt test
	@echo "All checks passed"
