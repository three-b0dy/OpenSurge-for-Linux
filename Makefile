.PHONY: test build doctor status policy-control-test installer-test
.PHONY: web-install web-build web-test control-build control-run
.PHONY: linux-lab-test linux-lab-test-tun linux-real-device-smoke systemd-unit-test linux-release-deps deb linux-ci-check

test:
	go test ./...

build:
	go build -o bin/opensurge ./cmd/opensurge

web-install:
	cd web && pnpm install

web-build:
	cd web && pnpm run build

web-test:
	cd web && pnpm run test

control-build: web-build
	go build -o bin/opensurge-control ./cmd/opensurge-control
	go build -o bin/opensurge-gateway ./cmd/opensurge-gateway

control-run: control-build
	./bin/opensurge-control --config examples/config.example.yaml

doctor:
	go run ./cmd/opensurge doctor --config examples/config.example.yaml

status:
	go run ./cmd/opensurge status --config examples/config.example.yaml

policy-control-test:
	./tests/integration/policy-control.sh

installer-test:
	bash tests/installer/opensurge-install_test.sh

linux-lab-test:
	bash tests/linux-lab/lab.sh test

linux-lab-test-tun:
	bash tests/linux-lab/lab.sh test-tun

linux-real-device-smoke:
	@if [ -n "$${UPSTREAM_IFACE:-}" ] || [ -n "$${DOWNSTREAM_IFACE:-}" ] || [ -n "$${DOWNSTREAM_VLAN:-}" ] || [ -n "$${LAN_CIDR:-}" ] || [ -n "$${MODE:-}" ]; then \
		bash tests/linux-real-device/smoke.sh; \
	else \
		bash tests/linux-real-device/smoke.sh --help; \
	fi

systemd-unit-test:
	bash tests/systemd/units_test.sh

linux-release-deps:
	@test -n "$(ARCH)" || (echo "ARCH must be amd64 or arm64" >&2; exit 2)
	bash scripts/prepare-linux-release-deps.sh "$(ARCH)"

deb:
	@test -n "$(ARCH)" || (echo "ARCH must be amd64 or arm64" >&2; exit 2)
	@test -n "$(VERSION)" || (echo "VERSION is required" >&2; exit 2)
	bash packaging/debian/build-deb.sh "$(ARCH)" "$(VERSION)"

linux-ci-check:
	bash scripts/check-linux-repository.sh
	bash tests/systemd/units_test.sh
	bash -n scripts/*.sh packaging/debian/build-deb.sh packaging/debian/DEBIAN/* tests/installer/*.sh tests/scripts/*.sh tests/packages/*.sh
