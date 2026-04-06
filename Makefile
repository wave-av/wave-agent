# WAVE Agent Build System
# Cross-compiles for all supported edge platforms

VERSION := 0.1.0
BINARY := wave-agent
LDFLAGS := -s -w -X main.Version=$(VERSION)

.PHONY: all clean build-all build-arm64 build-armv7 build-amd64 build-darwin

all: build-all

# Build for all platforms
build-all: build-arm64 build-armv7 build-amd64

# Raspberry Pi 5, Pi Zero 2W (64-bit), RK3328 SBC
build-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 .

# Older Pi devices (32-bit fallback)
build-armv7:
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-armv7 .

# x86_64 servers
build-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 .

# macOS (development)
build-darwin:
	GOOS=darwin GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 .

# Docker build (for CI)
docker-build:
	docker run --rm -v "$(PWD)":/app -w /app golang:1.23-alpine \
		sh -c "CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags='$(LDFLAGS)' -o dist/$(BINARY)-linux-arm64 ."

# Install on current device
install: build-arm64
	sudo cp dist/$(BINARY)-linux-arm64 /usr/local/bin/wave-agent
	sudo chmod +x /usr/local/bin/wave-agent
	sudo cp wave-agent.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable wave-agent
	@echo "WAVE Agent installed. Start with: sudo systemctl start wave-agent"

clean:
	rm -rf dist/

# Show binary sizes
sizes:
	@ls -lh dist/ 2>/dev/null || echo "No binaries built. Run: make build-all"
