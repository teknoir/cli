#!/bin/bash
# Runs inside the test container (see README.md): creates the device login users, one per sudo
# state, and starts sshd in the foreground. Nothing here is used outside the test.
#   TN_PASSWORD  the password of every user (also what the PASSWD rules ask for)
#   TN_PUBKEY    the public key that may log in as any of them
# /etc/tnctl-rig-features lists what this device has, one word per line: `sudo` or `sudo-rs`,
# `sudo-<version>`, and `faillock` (pam_faillock counts failed logins: 3 lock the account). The
# tests skip what a device cannot be set up for (sudo-rs has no logfile, log_input, requiretty or
# timestamp_type).
set -euo pipefail
: "${TN_PASSWORD:?}" "${TN_PUBKEY:?}"

features=/etc/tnctl-rig-features
if sudo --version 2>&1 | grep -qi sudo-rs; then
  rs=1
  echo sudo-rs > "$features"
else
  rs=
  echo sudo > "$features"
  echo "sudo-$(sudo --version | sed -n '1s/.* //p')" >> "$features"
  # What sudo did, for the tests to read: one entry per command and per failed password.
  printf '%s\n' 'Defaults logfile=/var/log/sudo.log' 'Defaults loglinelen=0' > /etc/sudoers.d/00-tnctl-test-log
fi
: > /var/log/sudo.log

uid=2000
# add <user> <login shell> <in group sudo: yes|no>
add() {
  uid=$((uid + 1))
  useradd --create-home --uid "$uid" --shell "/bin/$2" "$1"
  printf '%s:%s\n' "$1" "$TN_PASSWORD" | chpasswd
  [ "$3" = no ] || usermod -aG sudo "$1"
}
rule() { printf '%s\n' "$2" > "/etc/sudoers.d/$1"; }

for shell in bash zsh; do
  # pw: the normal device. The bootstrap puts the login in the sudo group, and the distro's
  #     "%sudo ALL=(ALL:ALL) ALL" asks for the password.
  # np: a login without a password on the platform: the bootstrap's drop-in on top of the group.
  # nt: password sudo that keeps no ticket (timestamp_timeout=0).
  # gl: password sudo with one ticket for all of the user's sessions (timestamp_type=global).
  for kind in pw np nt gl; do
    [ -z "$rs" ] || [ "$kind" != gl ] || continue
    user=$kind-$shell
    add "$user" "$shell" yes
    case $kind in
      np) rule "90-teknoir-platform-user-$uid" "#$uid ALL=(ALL:ALL) NOPASSWD: ALL" ;;
      nt) rule "50-$user" "Defaults:$user timestamp_timeout=0" ;;
      gl) rule "50-$user" "Defaults:$user timestamp_type=global" ;;
    esac
  done
done

# Rules that name commands. tnctl runs every root command as `sh -c`, so sh is the command.
# sh-np: the password for everything, but not for sh: `sudo -n true` asks, the real command does not.
# k3-np: the password for everything, but not for k3s: sh needs the password.
# id-pw: only id, with the password: sh is not allowed.
# id-np: only id, without a password: sh is not allowed, and `sudo -v` has nothing to check.
# ns:    not in the sudoers at all.
add sh-np bash yes
rule 50-sh-np 'sh-np ALL=(ALL:ALL) NOPASSWD: /usr/bin/sh, /bin/sh'
add k3-np bash yes
rule 50-k3-np 'k3-np ALL=(ALL:ALL) NOPASSWD: /usr/local/bin/k3s'
add id-pw bash no
rule 50-id-pw 'id-pw ALL=(ALL:ALL) /usr/bin/id'
add id-np bash no
rule 50-id-np 'id-np ALL=(ALL:ALL) NOPASSWD: /usr/bin/id'
add ns bash no
# bs:    password sudo on a device whose sh is bash (here: a sh of the login's own, first in its
#        PATH). bash execs the last command of a -c script, which dash does not do for this one.
add bs zsh yes
mkdir -p /home/bs/bin
ln -s /bin/bash /home/bs/bin/sh
echo 'PATH=$HOME/bin:$PATH' > /home/bs/.zshenv
chown -R bs: /home/bs
# fl-a, fl-b, fl-c: password sudo; the logins the pam_faillock tests count and lock.
for u in fl-a fl-b fl-c; do add "$u" bash yes; done
# tp, rp: password sudo that asks for root's password instead of the login's (targetpw, rootpw).
#         Nobody has root's password here: the login's is the wrong answer, as on such a device.
add tp bash yes
rule 50-tp 'Defaults:tp targetpw'
add rp bash yes
rule 50-rp 'Defaults:rp rootpw'
if [ -z "$rs" ]; then
  # rt:    password sudo that wants a terminal (requiretty).
  # li-pw: password sudo that logs the command's input (log_input).
  # li-np: passwordless sudo that logs the command's input.
  add rt bash yes
  rule 50-rt 'Defaults:rt requiretty'
  add li-pw bash yes
  add li-np zsh yes
  rule 50-li 'Defaults:li-pw,li-np log_input'
  rule 51-li-np 'li-np ALL=(ALL:ALL) NOPASSWD: ALL'
fi
chmod 0440 /etc/sudoers /etc/sudoers.d/*
visudo -c -q

# pam_faillock the way its manual sets it up, with its defaults spelled out (3 failures in 900 s
# lock the account for 600 s), and without the `account` line that would clear the count at every
# ssh login: nothing but a sudo login that works resets it here.
if command -v faillock >/dev/null 2>&1 && [ -f /etc/pam.d/common-auth ]; then
  cat > /etc/pam.d/common-auth <<'PAM'
auth    required                        pam_faillock.so preauth silent deny=3 fail_interval=900 unlock_time=600
auth    [success=1 default=ignore]      pam_unix.so nullok
auth    [default=die]                   pam_faillock.so authfail deny=3 fail_interval=900 unlock_time=600
auth    sufficient                      pam_faillock.so authsucc deny=3 fail_interval=900 unlock_time=600
auth    requisite                       pam_deny.so
auth    required                        pam_permit.so
PAM
  echo faillock >> "$features"
fi

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
