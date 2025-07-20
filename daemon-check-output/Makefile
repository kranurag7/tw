BINARY_NAME := daemon-check-output

.PHONY: build
build:
	go build -o $(BINARY_NAME) .

.PHONY: clean
clean:
	rm -f $(BINARY_NAME)

.PHONY: test
test:
	go test ./...

.PHONY: install
install: build
	cp $(BINARY_NAME) /usr/local/bin/

.PHONY: uninstall
uninstall:
	rm -f /usr/local/bin/$(BINARY_NAME)

.PHONY: all
all: build