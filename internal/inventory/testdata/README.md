# Образцы `internal/inventory`

Каждый каталог — корень файловой системы, с которого `Source.Collect` строит инвентарь; `golden/<имя>.json` —
ожидаемое тело (`go test ./internal/inventory -run TestCollectGolden -update` переписывает).

| Образец | Происхождение | Что покрывает |
|---|---|---|
| `pve-vm/` | **снят** скриптом (только то, что читает не-root, с воссозданием sysfs-ссылок) с гостевой ВМ Proxmox (Debian 12, Q35, SeaBIOS) | virtio-scsi «drive-scsi0», мосты Docker и compose `br-<hash>`, veth, QEMU tablet на USB |
| `pve-robot-debian13/` | **снят** тем же скриптом с тестового робота pve-robot-1 (гость Proxmox, Debian 13) | ВМ Proxmox по drive-id и OUI `bc:24:11` |
| `debian13-amd64/` | **снят** с железа: хост Proxmox VE на Debian 13 (Huananzhi X99, 2×Xeon) | NVMe + LVM, UEFI с переменной Secure Boot, Realtek, хабы Intel USB, tap/vmbr, compose-мосты |
| `jetson-l4t-r32/` | **снят** с Jetson Nano (L4T R32.6.1, ядро 4.9); `sys/devices/system/memory` доснят 2026-10-21 | device tree вместо DMI, GPU gm20b как platform-устройство, корень на USB-SSD за хабом, Intel Wi-Fi/BT, ядра 2–3 бывают offline, `/media/<user>/cidata`, RAM по блокам памяти (4 × 1 ГиБ) |

Синтетические случаи, которых нет ни на одной машине (UVC-камера, NVIDIA GPU, SD/eMMC, Secure Boot,
флаги IPv6, bind-монтажи песочницы systemd, подтома btrfs, блоки памяти), собраны в тестах из
`fstest.MapFS`, а не в каталогах.

## Обезличивание

- Серийные номера заменены заглушками `FIXTURE-…` (тест проверяет, что они уходят только хэшем).
- MAC-адреса настоящих сетевых карт: OUI (первые три октета) сохранён — по нему определяется
  производитель и ВМ Proxmox, — хвост заменён на `00:00:0N` (`00:04:4b:00:00:01` Jetson eth0,
  `9c:da:3e:00:00:02` Jetson wlan0, `bc:24:11:00:00:03` virtio eth0 ВМ). Случайные адреса
  мостов, veth и USB-гаджетов (бит locally administered) оставлены: они не идентифицируют машину.
- `pci.ids` — подмножество для устройств образца.
