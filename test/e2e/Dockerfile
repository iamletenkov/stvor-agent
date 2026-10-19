# Machine with systemd as PID 1 for the end-to-end test of stvor-agent (plan
# Task 20, spec §15.3); the same recipe as cream's test/integration/Dockerfile.
# curl, tar, sha256sum — the commands the teleport extension declares; jq —
# parsing JSON in the scenario; dbus — systemd-analyze security over the bus.
# No console or login: getty and logind are masked.
ARG BASE=debian:12
FROM ${BASE}
ENV DEBIAN_FRONTEND=noninteractive container=docker
RUN apt-get update \
	&& apt-get install -y --no-install-recommends systemd systemd-sysv ca-certificates curl jq procps dbus \
	&& apt-get clean \
	&& rm -rf /var/lib/apt/lists/* \
	&& systemctl mask getty.target console-getty.service systemd-logind.service systemd-firstboot.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
