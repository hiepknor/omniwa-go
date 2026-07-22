FROM golang:1.25.12-alpine AS build

RUN apk update && apk add --no-cache git build-base libjpeg-turbo-dev libwebp-dev

WORKDIR /build

# Copiar apenas arquivos de dependências primeiro para cachear o download
COPY go.mod go.sum ./

# whatsmeow agora vem do proxy oficial (go.mau.fi/whatsmeow, sem replace local) —
# não há mais submódulo whatsmeow-lib para copiar.
RUN go mod download

# Copiar o restante do código
COPY . .

ARG VERSION=dev
ARG REVISION=unknown
RUN CGO_ENABLED=1 go build -ldflags "-X main.version=${VERSION} -X main.revision=${REVISION}" -o server ./cmd/evolution-go

FROM alpine:3.24.1 AS final

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.source="https://github.com/hiepknor/omniwa-go"

# poppler-utils provides pdftoppm, used to rasterize PDF page 1 for /send/media document thumbnails
RUN apk update && apk add --no-cache tzdata ffmpeg libjpeg-turbo libwebp poppler-utils \
    && addgroup -S -g 10001 omniwa \
    && adduser -S -D -H -u 10001 -G omniwa omniwa \
    && mkdir -p /app/dbdata /app/logs \
    && chown -R 10001:10001 /app

WORKDIR /app

COPY --chown=10001:10001 --from=build /build/server .
COPY --chown=10001:10001 --from=build /build/manager/dist ./manager/dist
COPY --chown=10001:10001 --from=build /build/VERSION ./VERSION

ENV TZ=America/Sao_Paulo

USER 10001:10001

ENTRYPOINT ["/app/server"]
