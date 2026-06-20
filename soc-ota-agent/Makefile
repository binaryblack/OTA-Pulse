DESTDIR ?= /
prefix ?= $(DESTDIR)
bindir=/usr/bin
datadir ?= /usr/share
sysconfdir ?= /etc
systemd_unitdir ?= /lib/systemd
docexamplesdir ?= /usr/share/doc/otapulse-client/examples

# Binary configuration
BINARY_NAME ?= otapulse
LEGACY_BINARY_NAME = mender

GO ?= go
GOFMT ?= gofmt
V ?=
PKGS = $(shell go list ./... | grep -v vendor)
PKGFILES = $(shell find . \( -path ./vendor -o -path ./Godeps \) -prune \
		-o -type f -name '*.go' -print)
PKGFILES_notest = $(shell echo $(PKGFILES) | tr ' ' '\n' | grep -v '\(client/test\|_test.go\)' )
GOCYCLO ?= 15

CGO_ENABLED=1
export CGO_ENABLED

# Get rid of useless warning in lmdb
CGO_CFLAGS ?= -Wno-implicit-fallthrough -Wno-stringop-overflow
export CGO_CFLAGS

TOOLS = \
	github.com/fzipp/gocyclo/... \
	gitlab.com/opennota/check/cmd/varcheck \
	github.com/mendersoftware/deadcode \
	github.com/mendersoftware/gobinarycoverage \
	github.com/jstemmer/go-junit-report

VERSION = $(shell git describe --tags --dirty --exact-match 2>/dev/null || git rev-parse --short HEAD)

# Allow external GO_LDFLAGS from environment (e.g., from Yocto recipe)
# Default adds version info; external flags are appended
EXTRA_GO_LDFLAGS ?=
GO_LDFLAGS = \
	-ldflags "-X github.com/binaryblack/OTA-Pulse/conf.Version=$(VERSION) $(EXTRA_GO_LDFLAGS)"

# Allow external GO_BUILD_FLAGS from environment (e.g., -trimpath for Yocto)
# These are added to the go build command
GO_BUILD_FLAGS ?=

ifeq ($(V),1)
BUILDV = -v
endif

TAGS =
ifeq ($(LOCAL),1)
TAGS += local
endif

ifneq ($(TAGS),)
BUILDTAGS = -tags '$(TAGS)'
endif

IDENTITY_SCRIPTS = \
	support/otapulse-device-identity

INVENTORY_SCRIPTS = \
	support/otapulse-inventory-bootloader-integration \
	support/otapulse-inventory-hostinfo \
	support/otapulse-inventory-network \
	support/otapulse-inventory-os \
	support/otapulse-inventory-provides \
	support/otapulse-inventory-rootfs-type \
	support/otapulse-inventory-update-modules

INVENTORY_NETWORK_SCRIPTS = \
	support/otapulse-inventory-geo

MODULES = \
	support/modules/deb \
	support/modules/docker \
	support/modules/directory \
	support/modules/single-file \
	support/modules/rpm \
	support/modules/script

MODULES_ARTIFACT_GENERATORS = \
	support/modules-artifact-gen/docker-artifact-gen \
	support/modules-artifact-gen/directory-artifact-gen \
	support/modules-artifact-gen/single-file-artifact-gen

DBUS_POLICY_FILES = \
	support/dbus/io.otapulse.AuthenticationManager.conf \
	support/dbus/io.otapulse.UpdateManager.conf

build: $(BINARY_NAME)

$(BINARY_NAME): $(PKGFILES)
	$(GO) build $(GO_BUILD_FLAGS) $(GO_LDFLAGS) $(BUILDV) $(BUILDTAGS) -o $(BINARY_NAME)

# soc-ota-tunneld — statically linked, CGO-free reverse-SSH tunnel supervisor.
# Kept separate from the main CGO agent so it has minimal RSS and independent
# lifecycle.  CGO_ENABLED=0 is enforced here regardless of the top-level
# CGO_ENABLED setting; -s -w strips symbols.
TUNNELD_BINARY_NAME ?= soc-ota-tunneld
build-tunneld:
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -ldflags "-s -w" -o $(TUNNELD_BINARY_NAME) ./cmd/soc-ota-tunneld

# Legacy target for backward compatibility
mender: $(BINARY_NAME)
	@echo "Building legacy 'mender' target (now builds '$(BINARY_NAME)')"

install: install-bin \
	install-conf \
	install-dbus \
	install-examples \
	install-identity-scripts \
	install-inventory-scripts \
	install-modules \
	install-systemd

install-bin: $(BINARY_NAME)
	install -m 755 -d $(prefix)$(bindir)
	install -m 755 $(BINARY_NAME) $(prefix)$(bindir)/
	# Create legacy symlink for backward compatibility
	ln -sf $(BINARY_NAME) $(prefix)$(bindir)/$(LEGACY_BINARY_NAME)

install-conf:
	install -m 755 -d $(prefix)$(sysconfdir)/otapulse

install-datadir:
	install -m 755 -d $(prefix)$(datadir)/otapulse

install-dbus: install-datadir
	install -m 755 -d $(prefix)$(datadir)/dbus-1/system.d
	install -m 644 $(DBUS_POLICY_FILES) $(prefix)$(datadir)/dbus-1/system.d/

install-identity-scripts: install-datadir
	install -m 755 -d $(prefix)$(datadir)/otapulse/identity
	install -m 755 $(IDENTITY_SCRIPTS) $(prefix)$(datadir)/otapulse/identity/

install-inventory-scripts: install-inventory-local-scripts install-inventory-network-scripts

install-inventory-local-scripts: install-datadir
	install -m 755 -d $(prefix)$(datadir)/otapulse/inventory
	install -m 755 $(INVENTORY_SCRIPTS) $(prefix)$(datadir)/otapulse/inventory/

install-inventory-network-scripts: install-datadir
	install -m 755 -d $(prefix)$(datadir)/otapulse/inventory
	install -m 755 $(INVENTORY_NETWORK_SCRIPTS) $(prefix)$(datadir)/otapulse/inventory/

install-modules:
	install -m 755 -d $(prefix)$(datadir)/otapulse/modules/v3
	install -m 755 $(MODULES) $(prefix)$(datadir)/otapulse/modules/v3/

install-modules-gen:
	install -m 755 -d $(prefix)$(bindir)
	install -m 755 $(MODULES_ARTIFACT_GENERATORS) $(prefix)$(bindir)/

install-systemd:
	install -m 755 -d $(prefix)$(systemd_unitdir)/system
	install -m 0644 support/otapulse-client.service $(prefix)$(systemd_unitdir)/system/

install-examples:
	install -m 755 -d $(prefix)$(docexamplesdir)
	install -m 0644 support/demo.crt $(prefix)$(docexamplesdir)/

uninstall: uninstall-bin \
	uninstall-conf \
	uninstall-dbus \
	uninstall-identity-scripts \
	uninstall-inventory-scripts \
	uninstall-modules \
	uninstall-modules-gen \
	uninstall-systemd \
	uninstall-examples

uninstall-bin:
	rm -f $(prefix)$(bindir)/$(BINARY_NAME)
	rm -f $(prefix)$(bindir)/$(LEGACY_BINARY_NAME)
	-rmdir -p $(prefix)$(bindir)

uninstall-conf:
	-rmdir -p $(prefix)$(sysconfdir)/otapulse

uninstall-dbus:
	for policy in $(DBUS_POLICY_FILES); do \
		rm -f $(prefix)$(datadir)/dbus-1/system.d/$$(basename $$policy); \
	done
	-rmdir -p $(prefix)$(datadir)/dbus-1/system.d

uninstall-identity-scripts:
	for script in $(IDENTITY_SCRIPTS); do \
		rm -f $(prefix)$(datadir)/otapulse/identity/$$(basename $$script); \
	done
	-rmdir -p $(prefix)$(datadir)/otapulse/identity

uninstall-inventory-scripts: uninstall-inventory-local-scripts uninstall-inventory-network-scripts
	-rmdir -p $(prefix)$(datadir)/otapulse/inventory

uninstall-inventory-local-scripts:
	for script in $(INVENTORY_SCRIPTS); do \
		rm -f $(prefix)$(datadir)/otapulse/inventory/$$(basename $$script); \
	done
	-rmdir -p $(prefix)$(datadir)/otapulse/inventory

uninstall-inventory-network-scripts:
	for script in $(INVENTORY_NETWORK_SCRIPTS); do \
		rm -f $(prefix)$(datadir)/otapulse/inventory/$$(basename $$script); \
	done
	-rmdir -p $(prefix)$(datadir)/otapulse/inventory

uninstall-modules:
	for script in $(MODULES); do \
		rm -f $(prefix)$(datadir)/otapulse/modules/v3/$$(basename $$script); \
	done
	-rmdir -p $(prefix)$(datadir)/otapulse/modules/v3

uninstall-modules-gen:
	for script in $(MODULES_ARTIFACT_GENERATORS); do \
		rm -f $(prefix)$(bindir)/$$(basename $$script); \
	done
	-rmdir -p $(prefix)$(bindir)

uninstall-systemd:
	rm -f $(prefix)$(systemd_unitdir)/system/otapulse-client.service
	-rmdir -p $(prefix)$(systemd_unitdir)/system

uninstall-examples:
	rm -f $(prefix)$(docexamplesdir)/demo.crt
	-rmdir -p $(prefix)$(docexamplesdir)

clean:
	$(GO) clean
	rm -f coverage.txt
	rm -f $(BINARY_NAME) $(LEGACY_BINARY_NAME) $(TUNNELD_BINARY_NAME)

get-tools:
	set -e ; for t in $(TOOLS); do \
		echo "-- go getting $$t"; \
		GO111MODULE=off go get -u $$t; \
	done

check: test extracheck

test:
	$(GO) test $(BUILDV) $(PKGS)

extracheck: gofmt govet godeadcode govarcheck gocyclo
	echo "All extra-checks passed!"

gofmt:
	echo "-- checking if code is gofmt'ed"
	if [ -n "$$($(GOFMT) -d $(PKGFILES))" ]; then \
		"$$($(GOFMT) -d $(PKGFILES))" \
		echo "-- gofmt check failed"; \
		/bin/false; \
	fi

govet:
	echo "-- checking with govet"
	$(GO) vet -composites=false -unsafeptr=false ./...

godeadcode:
	echo "-- checking for dead code"
	deadcode -ignore version.go:Version

govarcheck:
	echo "-- checking with varcheck"
	varcheck ./...

gocyclo:
	echo "-- checking cyclometric complexity > $(GOCYCLO)"
	gocyclo -over $(GOCYCLO) $(PKGFILES_notest)

cover: coverage
	$(GO) tool cover -func=coverage.txt

htmlcover: coverage
	$(GO) tool cover -html=coverage.txt

coverage:
	rm -f coverage.txt
	$(GO) test -v -coverprofile=coverage-tmp.txt -coverpkg=./... ./... > .tmp.go-test.txt ; echo $$? > .tmp.return-code.txt
	cat .tmp.go-test.txt
	go-junit-report < .tmp.go-test.txt > report.xml
	rm -f .tmp.go-test.txt
	if [ -f coverage-missing-subtests.txt ]; then \
		echo 'mode: set' > coverage.txt; \
		cat coverage-tmp.txt coverage-missing-subtests.txt | grep -v 'mode: set' >> coverage.txt; \
	else \
		mv coverage-tmp.txt coverage.txt; \
	fi
	rm -f coverage-tmp.txt coverage-missing-subtests.txt
	failing_tests=$$(grep -Po '(?<=failures=")\d+' report.xml | awk '{s+=$$1} END {print s}'); \
	echo FAILING TESTS: $$failing_tests
	return_code=$$(cat .tmp.return-code.txt); \
	rm -f .tmp.return-code.txt; \
	exit $$return_code

instrument-binary:
	# Patch the client to make it ready for coverage analysis
	git apply patches/0001-Instrument-Mender-client-for-coverage-analysis.patch
	# Then instrument the files with the gobinarycoverage tool
	gobinarycoverage github.com/mendersoftware/mender

.PHONY: build
.PHONY: build-tunneld
.PHONY: clean
.PHONY: get-tools
.PHONY: test
.PHONY: check
.PHONY: cover
.PHONY: htmlcover
.PHONY: coverage
.PHONY: install
.PHONY: install-bin
.PHONY: install-conf
.PHONY: install-datadir
.PHONY: install-dbus
.PHONY: install-identity-scripts
.PHONY: install-inventory-scripts
.PHONY: install-inventory-local-scripts
.PHONY: install-inventory-network-scripts
.PHONY: install-modules
.PHONY: install-modules-gen
.PHONY: install-systemd
.PHONY: install-examples
.PHONY: uninstall
.PHONY: uninstall-bin
.PHONY: uninstall-conf
.PHONY: uninstall-dbus
.PHONY: uninstall-identity-scripts
.PHONY: uninstall-inventory-scripts
.PHONY: uninstall-inventory-local-scripts
.PHONY: uninstall-inventory-network-scripts
.PHONY: uninstall-modules
.PHONY: uninstall-modules-gen
.PHONY: uninstall-systemd
.PHONY: uninstall-examples
.PHONY: instrument-binary
