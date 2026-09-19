# Сборка keel и раздача готового бинарника.
#
# Две стадии и ничего лишнего:
#
#   builder — собирает статические бинарники. CGO выключен нарочно: на
#             хосте Proxmox не должно найтись ни одной причины, по которой
#             keel не запустится. Инструмент восстановления, которому нужна
#             «правильная» libc, — плохой инструмент восстановления.
#   dist    — раздаёт бинарники, контрольные суммы и install.sh по HTTP.
#             TLS, домен и маршрутизацию делает Traefik, который в Dokploy
#             уже есть, поэтому свой обратный прокси здесь не поднимается.

FROM golang:1.24-alpine AS builder

ARG VERSION=dev
# KEEL_DIST_URL вкомпилируется в бинарник: по нему keel update будет
# искать свои же обновления. Без него обновляться неоткуда.
ARG KEEL_DIST_URL=https://keel.example.invalid

WORKDIR /src

# Зависимости отдельным слоем: он переживает правки кода и не пересобирается.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# -trimpath убирает из бинарника пути сборочной машины: они бесполезны
# тому, кто будет читать панику на своём хосте, и лишнее о нас рассказывают.
RUN set -eu; \
    for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
        go build -trimpath \
          -ldflags "-s -w -X main.version=${VERSION} -X github.com/vshivtsev-dev/keel/internal/cli.DistURL=${KEEL_DIST_URL}" \
          -o "/out/keel-linux-$arch" ./cmd/keel; \
    done

# Проверка на месте сборки: собранный бинарник должен хотя бы отвечать.
# Поймать здесь дешевле, чем на хосте, который только что переустановили.
RUN /out/keel-linux-amd64 version

WORKDIR /out
RUN sha256sum keel-linux-* > checksums.txt && cat checksums.txt


FROM nginx:alpine AS dist

ARG VERSION=dev
ARG KEEL_DIST_URL=https://keel.example.invalid
LABEL org.opencontainers.image.title="keel" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.description="Сборка и восстановление хоста Proxmox VE по описанию"

COPY build/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /out/ /srv/keel/
COPY install.sh /srv/keel/install.sh

# Адрес раздачи подставляется в установщик: человек копирует одну строку
# из README, и та должна работать без правки.
RUN sed -i "s|^KEEL_DIST=\"\${KEEL_DIST:-.*}\"|KEEL_DIST=\"\${KEEL_DIST:-${KEEL_DIST_URL}}\"|" /srv/keel/install.sh \
 && printf '%s\n' "${VERSION}" > /srv/keel/VERSION \
 && chmod -R a+r /srv/keel \
 && grep -q "${KEEL_DIST_URL}" /srv/keel/install.sh

EXPOSE 80
