#!/bin/bash
# Runs inside the test container (see README.md): creates the device login users, one per sudo
# state and login shell, and starts sshd in the foreground. Nothing here is used outside the test.
#   TN_PASSWORD  the password of every user (also what the PASSWD rules ask for)
#   TN_PUBKEY    the public key that may log in as any of them
set -euo pipefail
: "${TN_PASSWORD:?}" "${TN_PUBKEY:?}"

# What sudo did, for the tests to read: one entry per command and per failed password.
printf '%s\n' 'Defaults logfile=/var/log/sudo.log' 'Defaults loglinelen=0' > /etc/sudoers.d/00-tnctl-test-log
: > /var/log/sudo.log

uid=2000
for shell in bash zsh; do
  # pw: the normal device. The bootstrap puts the login in the sudo group, and the distro's
  #     "%sudo ALL=(ALL:ALL) ALL" asks for the password.
  # np: a login without a password on the platform: the bootstrap's drop-in on top of the group.
  # nt: password sudo that keeps no ticket (timestamp_timeout=0).
  # gl: password sudo with one ticket for all of the user's sessions (timestamp_type=global).
  for kind in pw np nt gl; do
    user=$kind-$shell
    uid=$((uid + 1))
    useradd --create-home --uid "$uid" --shell "/bin/$shell" --groups sudo "$user"
    printf '%s:%s\n' "$user" "$TN_PASSWORD" | chpasswd
    case $kind in
      np) echo "#$uid ALL=(ALL:ALL) NOPASSWD: ALL" > "/etc/sudoers.d/90-teknoir-platform-user-$uid" ;;
      nt) echo "Defaults:$user timestamp_timeout=0" > "/etc/sudoers.d/50-$user" ;;
      gl) echo "Defaults:$user timestamp_type=global" > "/etc/sudoers.d/50-$user" ;;
    esac
  done
done
chmod 0440 /etc/sudoers.d/*
visudo -c -q

printf '%s\n' "$TN_PUBKEY" > /etc/ssh/authorized_keys
chmod 0644 /etc/ssh/authorized_keys
cat > /etc/ssh/sshd_config.d/tnctl-test.conf <<'CONF'
AuthorizedKeysFile /etc/ssh/authorized_keys
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
MaxSessions 20
MaxStartups 50:30:100
CONF
# A container has no login uid to set.
sed -i 's/^session\s\+required\s\+pam_loginuid.so/session optional pam_loginuid.so/' /etc/pam.d/sshd
ssh-keygen -A >/dev/null
mkdir -p /run/sshd
exec /usr/sbin/sshd -D -e
