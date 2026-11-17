VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BUILD   := CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)"

.PHONY: build android linux test lint fmt clean

build:
	$(BUILD) -o dist/edgephone ./cmd/edgephone

android:
	GOOS=android GOARCH=arm64 $(BUILD) -o dist/edgephone_android_arm64 ./cmd/edgephone

linux:
	GOOS=linux GOARCH=arm64 $(BUILD) -o dist/edgephone_linux_arm64 ./cmd/edgephone

test:
	go test -race -count=1 ./...

lint:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

fmt:
	gofmt -w .

clean:
	rm -rf dist
