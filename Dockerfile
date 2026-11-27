# The MCP server as one static binary on distroless: no shell, no package manager, no
# libc, a non-root user. Bases are pinned by digest, Go dependencies by go.sum. It needs
# no GPU, devices or journal: it talks to health-validation serve on loopback. See
# docs/security.md to run it.
FROM golang:1.27.4-trixie@sha256:430985542100b871e108ccc00e8dcc5eef08ca14ee5825e98ebf563d8ffec4ee AS build
WORKDIR /src
COPY go.mod go.sum version.txt ./
COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOTOOLCHAIN=local GOFLAGS=-mod=readonly go build -trimpath \
      -ldflags "-s -w -X main.version=$(cat version.txt)" \
      -o /nvidia-health-validation-mcp ./cmd/nvidia-health-validation-mcp

FROM gcr.io/distroless/static-debian13:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --chmod=555 --from=build /nvidia-health-validation-mcp /nvidia-health-validation-mcp
LABEL io.modelcontextprotocol.server.name="io.github.omerfsen/nvidia-health-validation-mcp"
USER 65532:65532
ENTRYPOINT ["/nvidia-health-validation-mcp"]
