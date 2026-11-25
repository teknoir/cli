# Throwaway image for the real-sudo tests of tnctl's root wrapper (see README.md).
# Debian trixie is what Teknoir virtual devices run: sudo 1.9.16, dash as sh, bash and zsh as
# login shells, OpenSSH 10.
FROM debian:trixie-slim
RUN apt-get update -qq \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      sudo zsh openssh-server procps >/dev/null \
 && rm -rf /var/lib/apt/lists/*
COPY setup.sh /setup.sh
CMD ["/setup.sh"]
