# Throwaway image for the real-sudo tests of tnctl's root wrapper (see README.md).
# The default, Debian trixie, is what Teknoir virtual devices run: sudo 1.9.16, dash as sh, bash
# and zsh as login shells, OpenSSH 10. run.sh builds it for other device systems too:
#   BASE  the device system (ubuntu:22.04 has sudo 1.9.9, ubuntu:26.04 has sudo-rs)
#   SUDO  the package that provides sudo: sudo, or sudo-rs (the default of Ubuntu 25.10 and later)
ARG BASE=debian:trixie-slim
FROM ${BASE}
ARG SUDO=sudo
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      ${SUDO} zsh openssh-server procps gzip >/dev/null \
 && rm -rf /var/lib/apt/lists/*
COPY setup.sh /setup.sh
CMD ["/setup.sh"]
