# Установка Proxmox VE

## Обычный способ: флешка

1. Скачать ISO с [proxmox.com/downloads](https://www.proxmox.com/downloads).
2. Записать на флешку: `dd if=proxmox-ve_*.iso of=/dev/sdX bs=1M status=progress`
   (на Windows — Rufus в режиме DD).
3. Загрузиться с неё, пройти установщик.

Что важно задать правильно с первого раза:

- **Файловая система.** Меняешь диск, а не переустанавливаешь с нуля — ставь
  ту же, что была. Была ZFS — ставь ZFS.
- **Имя хоста.** То же, что раньше, иначе перестанут работать закладки,
  сертификаты и ссылки в документации.
- **IP-адрес.** Тот же. Гости с фиксированными адресами и проброс портов на
  роутере завязаны на него.
- **E-mail.** На него уходят письма о проблемах с дисками. Не оставляй
  `mail@example.com` — предупреждение о сыплющемся диске стоит того, чтобы
  дойти до тебя.

После установки — [docs/00-RECOVERY.md](00-RECOVERY.md), шаг 2.

## Автоматический способ: answer.toml

Начиная с версии 8.2 у Proxmox есть штатная автоустановка: в ISO
подкладывается файл `answer.toml`, и установщик не задаёт ни одного вопроса.
Полезно, если хостов несколько или если переустанавливать приходится часто.

Почему это **не часть keel**: чтобы собрать такой ISO, нужна вторая рабочая
машина с `proxmox-auto-install-assistant`. А keel рассчитан на то, что кроме
самого хоста у тебя может не быть вообще ничего.

Как выглядит файл:

```toml
[global]
keyboard = "en-us"
country = "ru"
fqdn = "pve.local"
mailto = "ты@example.com"
timezone = "Europe/Moscow"
root_password = "смени-это"

[network]
source = "from-dhcp"

[disk-setup]
filesystem = "zfs"
zfs.raid = "raid1"
disk_list = ["sda", "sdb"]
```

Сборка ISO на другой машине с Debian:

```bash
apt install proxmox-auto-install-assistant
proxmox-auto-install-assistant prepare-iso proxmox-ve_9.iso \
  --fetch-from iso --answer-file answer.toml
```

Полученный ISO ставит систему без вопросов. Дальше — те же шаги
восстановления, начиная с установки keel.

Документация: [pve.proxmox.com/wiki/Automated_Installation](https://pve.proxmox.com/wiki/Automated_Installation)

**Осторожно:** в `answer.toml` лежит пароль root открытым текстом, а
`disk_list` означает, что эти диски будут стёрты без вопросов. Файл — не для
git и не для общей флешки.
