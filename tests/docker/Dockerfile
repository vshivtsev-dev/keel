# Окружение для прогона тестов keel.
#
# Основа — Debian 13 (Trixie), то же, на чём стоит Proxmox VE 9. Это не
# косметика: именно здесь проверяются допущения об окружении, ради которых
# и выбирался JSON вместо YAML — есть ли perl с JSON::PP, работает ли
# режим relaxed, на месте ли whiptail, как называется локаль C.UTF-8.
#
# Настоящие qm/pct/pvesm/pvesh внутрь не ставятся и не нужны: тесты
# подменяют их заглушками, которые записывают свои аргументы.

FROM debian:trixie-slim

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash \
      perl \
      whiptail \
      shellcheck \
      xz-utils \
      tar \
      gzip \
      curl \
      ca-certificates \
      diffutils \
      findutils \
      coreutils \
      locales \
 && rm -rf /var/lib/apt/lists/*

# Репозиторий монтируется только на чтение: тест, который пишет в дерево
# проекта, — это тест со скрытой зависимостью от предыдущего прогона.
WORKDIR /keel

ENTRYPOINT ["/bin/bash"]
CMD ["tests/run.sh"]
