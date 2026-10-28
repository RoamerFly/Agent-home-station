FROM --platform=$BUILDPLATFORM golang:1.25.3-alpine@sha256:aee43c3ccbf24fdffb7295693b6e33b21e01baec1b2a55acc351fde345e9ec34 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/wechat-sendd ./cmd/wechat-sendd \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/wechat-send ./cmd/wechat-send \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /out/wechat-login ./cmd/wechat-login

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/wechat-sendd /usr/local/bin/wechat-sendd
COPY --from=build /out/wechat-send /usr/local/bin/wechat-send
COPY --from=build /out/wechat-login /usr/local/bin/wechat-login
USER nonroot:nonroot
EXPOSE 8787
ENTRYPOINT ["/usr/local/bin/wechat-sendd"]
