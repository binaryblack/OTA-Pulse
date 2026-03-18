# Integration test Makefile for OTAPulse

SHELL := /bin/bash

.PHONY: all test test-debug test-quick server server-with-artifact clean

# Run integration tests (standalone, no QEMU)
all: test

test:
	@echo "Running integration tests..."
	bash test-qemu-ota.sh

# Run with server kept alive for debugging
test-debug:
	bash test-qemu-ota.sh --keep-server

# Run with pre-built binary
test-quick:
	bash test-qemu-ota.sh --skip-build

# Start mock server for manual testing
server:
	python3 mock-ota-server.py --port 8443 --no-tls

# Start mock server with artifact
server-with-artifact:
	@if [ -z "$(ARTIFACT)" ]; then \
		echo "Usage: make server-with-artifact ARTIFACT=/path/to/file.mender"; \
		exit 1; \
	fi
	python3 mock-ota-server.py --port 8443 --no-tls --artifact "$(ARTIFACT)"

clean:
	rm -rf /tmp/otapulse-integration-*
