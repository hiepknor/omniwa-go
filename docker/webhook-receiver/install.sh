#!/bin/sh
set -eu

if [ "$(id -u)" -ne 0 ]; then
    echo "install.sh must run as root" >&2
    exit 1
fi

source_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
install -d -o root -g root -m 0755 /opt/omniwa-webhook
install -o root -g root -m 0755 "$source_dir/receiver.py" /opt/omniwa-webhook/receiver.py
install -o root -g root -m 0755 "$source_dir/monitor.py" /opt/omniwa-webhook/monitor.py
install -o root -g root -m 0644 "$source_dir/omniwa-webhook-receiver.service" /etc/systemd/system/omniwa-webhook-receiver.service
install -o root -g root -m 0644 "$source_dir/omniwa-webhook-monitor.service" /etc/systemd/system/omniwa-webhook-monitor.service
install -o root -g root -m 0644 "$source_dir/omniwa-webhook-monitor.timer" /etc/systemd/system/omniwa-webhook-monitor.timer
systemctl daemon-reload

echo "Installed receiver and monitor artifacts. Configure credentials and drop-ins before starting services."
