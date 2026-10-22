# Образцы `internal/collect`

Каждый каталог — корень файловой системы (`proc/…`, `sys/…`), на котором тесты гоняют `Collector`
(`Root: os.DirFS(…)`). Ниже — откуда взят каждый образец: **снят** (скопирован с настоящей машины
только на чтение, без root) или **написан** (вручную по раскладке настоящей машины и форматам ядра;
числа выдуманы, но правдоподобны).

| Образец | Происхождение | Что покрывает |
|---|---|---|
| `pve-vm/` | **снят** с гостевой ВМ Proxmox (Debian 12, KVM, 12 vCPU) — dev-ВМ стенда | steal, PSI, нет cpufreq и thermal_throttle; Docker veth/мосты, overlay-монтажи, virtio_net со speed −1, нет термозон |
| `debian12-amd64/` | **написан** по раскладке Intel-железа на ядре 6.1 | cpufreq, thermal_throttle; PEAK USB CAN, NVMe + LVM + XFS, bind-монтаж, squashfs/loop/NFS, ro по суперблоку (`/backup`) и ro только на уровне монтажа (`/media/usb disk`), hwmon coretemp/nvme |
| `ubuntu2404-amd64/` | **написан** по раскладке AMD-железа на ядре 6.8 | нет thermal_throttle, reboot-required; Wi-Fi, WWAN, подтома btrfs, snap, `/media/<user>/…`, два NVMe hwmon |
| `jetson-l4t-r32/` | **написан** по раскладке Jetson Nano (L4T R32, ядро 4.9) | нет PSI, cpu3 offline; gs_usb CAN, eMMC с boot-областями, зоны L4T, атрибуты hwmon под `device/` (как в 4.9) |
| `jetson-nano-r32/` | **снят** 2026-10-21 с тестового Jetson Nano лаборатории (L4T R32.6.1, ядро 4.9.253, корень на USB-SSD) | настоящие `/proc` (stat, meminfo, diskstats, net/dev, net/wireless, self/mountinfo) и sysfs (net, block, thermal, hwmon, cpufreq); Intel Wi-Fi, USB-гаджеты rndis0/usb0, l4tbr0, `/media/<user>/cidata`, нет CAN, нет PSI, нет throttle |
| `pve-robot-debian13/` | **снят** 2026-10-22 скриптом `tools/collect-fixture.sh` с тестового робота pve-robot-1 (гость Proxmox, Debian 13, ядро 6.12, 2 vCPU, cgroup v2) | PSI есть, нет термозон, cpufreq и throttle; Docker `docker0`/`veth*`, CD-ROM `sr0`, `/boot/efi` на vfat, `reboot-required` |
| `sandbox-strict/` | **написан** по mountinfo службы под `ProtectSystem=strict`, `PrivateTmp`, `ReadWritePaths` | всё ro на уровне монтажа при rw-суперблоках, bind-монтажи `/tmp`, `/var/tmp`, `/var/lib/stvor-agent`, имена пользователей в точках монтирования, `fuseblk` |
| `netlink/container-getlink.bin` | **снят**: `syscall.NetlinkRIB(RTM_GETLINK)` в контейнере Docker | lo, eth0, без CAN |
| `netlink/*.bin` (остальные) | **написаны** функциями `build*` из `netlink_test.go` байт в байт по UAPI ядра; `TestNetlinkFixturesUpToDate` сверяет, `STVOR_UPDATE_NETLINK=1` переписывает | CAN (debian12, jetson), семейство nl80211 и его отсутствие, станция Wi-Fi |

## Обезличивание

Новые образцы снимает `tools/collect-fixture.sh <каталог>` на самой машине от обычного пользователя: он
копирует только читаемое сборщиком, подставляет выдуманный `boot_id` и заменяет имена пользователей
(uid 1000–59999 из `/etc/passwd`) в mountinfo на `user`; `TestCollectFixtureScript` сверяет, что снимок
даёт сборщику те же точки, что исходный корень.

- MAC-адресов в образцах `collect` нет (сборщик их не читает); `boot_id` — выдуманный или не снят.
- `jetson-nano-r32`: имя хоста и серийные номера не снимались; имя пользователя в
  `/media/nano/cidata` оставлено намеренно — тест проверяет, что наружу уходит `/media/*/cidata`.

## Чего нет ни в одном образце

Троттлинг CPU на Jetson: у L4T нет непривилегированного источника (счётчики `thermal_throttle` —
только Intel; состояние троттлинга — в debugfs и tegrastats, оба под root). Агент на Jetson не
отдаёт `cpu_throttle` и рядов `system.cpu.throttles` в этой версии.
