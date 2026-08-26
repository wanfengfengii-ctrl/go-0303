FROM golang:1.25.6 AS benzhi-build
ARG TARGETOS=linux
ARG TARGETARCH
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
ENV GOTOOLCHAIN=local
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
COPY go.mod go.sum ./
RUN mkdir -p /go/pkg/mod && go mod download
COPY web/package*.json ./web/
RUN cd web && npm ci
COPY . .
RUN cd web && npm run build
RUN go build ./...
RUN mkdir -p /out && CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" go build -trimpath -o /out/benzhi-app ./cmd/server

FROM golang:1.25.6 AS benzhi-runtime
LABEL io.benzhi.delivery-template="frontend-v2"
ENV GOTOOLCHAIN=local
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
COPY --from=benzhi-build /go/pkg/mod /go/pkg/mod
COPY --from=benzhi-build /src /app
COPY --from=benzhi-build /out/benzhi-app /usr/local/bin/benzhi-app
CMD ["/usr/local/bin/benzhi-app"]
