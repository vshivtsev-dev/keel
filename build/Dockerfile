# Сборка keel и раздача готового бинарника.
#
# Две стадии и ничего лишнего:
#
#   builder — собирает статический бинарник. CGO выключен нарочно: на хосте
#             Proxmox не должно найтись ни одной причины, по которой keel
#             не запустится. Инструмент восстановления, которому нужна
#             «правильная» libc, — плохой инструмент восстановления.
#   dist    — раздаёт бинарник, контрольные суммы и install.sh по HTTP.
#             TLS, домен и маршрутизацию делает Traefik, который в Dokploy
#             уже есть, поэтому свой обратный прокси здесь не поднимается.

FROM golang:1.24-alpine AS builder

ARG VERSION=dev
ARG TARGETARCH=amd64

WORKDIR /src

# Зависимости отдельным слоем: он переживает правки кода и не пересобирается.
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# -trimpath убирает из бинарника пути сборочной машины: они бесполезны
# тому, кто будет читать панику на своём хосте, и лишнее о нас рассказывают.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/keel-linux-${TARGETARCH} ./cmd/keel

# Проверка на месте сборки: собранный бинарник должен хотя бы отвечать.
# Поймать здесь дешевле, чем на хосте, который только что переустановили.
RUN /out/keel-linux-${TARGETARCH} version

WORKDIR /out
RUN sha256sum keel-linux-* > checksums.txt && cat checksums.txt


FROM nginx:alpine AS dist

ARG VERSION=dev
LABEL org.opencontainers.image.title="keel" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.description="Сборка и восстановление хоста Proxmox VE по описанию"

COPY build/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /out/ /srv/keel/
COPY install.sh /srv/keel/install.sh

# Ссылка без архитектуры: install.sh на обычном хосте берёт именно её.
RUN ln -sf keel-linux-amd64 /srv/keel/keel \
 && printf '%s\n' "${VERSION}" > /srv/keel/VERSION \
 && chmod -R a+r /srv/keel

EXPOSE 80
