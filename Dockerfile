FROM --platform=$BUILDPLATFORM golang:1.24-alpine@sha256:8bee1901f1e530bfb4a7850aa7a479d17ae3a18beb6e09064ed54cfd245b7191 AS build
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
