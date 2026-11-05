# Local checks, the same as CI (platform/.github/workflows/go.yml). Go is not required on the host:
# every target runs in the pinned tool image, Docker is enough. Testcontainers starts PostgreSQL
# through the Docker socket of the host.
GO_IMAGE   := golang:1.27.3
LINT_IMAGE := golangci/golangci-lint:v2.15.0
VULN       := golang.org/x/vuln/cmd/govulncheck@v1.9.0
COVERAGE   := 60

DOCKER := docker run --rm -v "$(CURDIR):/src" -v inspecto-gomod:/go/pkg/mod -w /src
TESTCONTAINERS := -v /var/run/docker.sock:/var/run/docker.sock \
	-e TESTCONTAINERS_HOST_OVERRIDE=host.docker.internal \
	-e TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock

.PHONY: lint fmt test vuln build check

lint:
	$(DOCKER) $(LINT_IMAGE) golangci-lint run ./...

fmt:
	$(DOCKER) $(LINT_IMAGE) golangci-lint fmt ./...

test:
	$(DOCKER) $(TESTCONTAINERS) $(GO_IMAGE) sh -ec '\
		go test -race -coverprofile=coverage.out ./...; \
		total=$$(go tool cover -func=coverage.out | awk "/^total:/ {sub(\"%\",\"\",\$$3); print \$$3}"); \
		echo "Total coverage: $${total}%"; \
		awk -v t="$$total" -v min=$(COVERAGE) "BEGIN { exit (t + 0 < min + 0) }"'

vuln:
	$(DOCKER) $(GO_IMAGE) sh -ec 'go install $(VULN) && govulncheck ./...'

build:
	$(DOCKER) $(GO_IMAGE) go build ./...

check: lint build test vuln
