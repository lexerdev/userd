NAME := userd

DEPS := go.mod go.sum main.go distro.go

build:
	go build -o dist/$(NAME)

# I bet there's a better way to do this, but I don't want to spend brain cells on this right now
all: dist/$(NAME)-linux-amd64 dist/$(NAME)-linux-arm64 dist/$(NAME)-darwin-amd64 dist/$(NAME)-darwin-arm64

dist/$(NAME)-linux-amd64: $(DEPS)
	GOOS=linux GOARCH=amd64 go build -o $@

dist/$(NAME)-linux-arm64: $(DEPS)
	mkdir -p $(dir $@)
	GOOS=linux GOARCH=arm64 go build -o $@

dist/$(NAME)-darwin-amd64: $(DEPS)
	mkdir -p $(dir $@)
	GOOS=darwin GOARCH=amd64 go build -o $@

dist/$(NAME)-darwin-arm64: $(DEPS)
	mkdir -p $(dir $@)
	GOOS=darwin GOARCH=arm64 go build -o $@

clean:
	rm -rf dist
-o $(dir $@)PHONY: clean
