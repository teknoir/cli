# The TeknoirBot's own computer

A TeknoirBot runs on a computer of its own: a virtual device named `tb-<bot>` in its teamspace. It is
a device like any other to the platform — same keys, MQTT login and tunnel machinery — but its
configuration belongs to the platform, and it runs a small k3s of its own that the agent may use.

Read this file when you are working *inside* the bot: its namespaces, its registry and builder, its
local MQTT broker, its desktop, or its agent's two modes.

## The computer

- The device is `tb-<bot>`: a KubeVirt virtual machine with a single-node k3s. `kubectl` has no
  kubeconfig context there, so every call names its namespace explicitly: `-n apps`.
- The bot's own name, teamspace and access are in `tnctl whoami -o json`; the device name is
  `tb-` followed by `.bot.name`.
- Names starting `tb-` are kept for these computers. A robot cannot use one as its device, and the
  portal will not change one.
- Its configuration belongs to the platform. Do not try to change the device's applications, labels
  or overrides; use the namespaces inside it instead.

## The environment

These are set for the agent's process:

| Variable | Meaning |
|---|---|
| `TEKNOIRBOT_API` | The bot API's base. `tnctl` takes its fleet root and identity from it. |
| `TEKNOIRBOT_NAMESPACE` | The bot's teamspace. It is the only one the bot works in. |
| `TEKNOIRBOT_NAME` | The bot's name; the computer is `tb-` + this. |
| `TEKNOIR_DOMAIN` | The platform's domain. Informational: it appears in `-o json`, and nothing is reached through it. |
| `TNCTL_BOT_KEY_FILE` | The file holding the bot's key. On a bot's computer: `/run/teknoirbot/key`. |
| `BUILDKIT_HOST` | The in-VM builder: `tcp://buildkitd.teknoir-build:1234`. |

Never change `TEKNOIRBOT_API`, `TEKNOIRBOT_NAMESPACE` or `TNCTL_BOT_KEY_FILE`. They are what make
`tnctl` a bot's tnctl; a command that sets or exports them is a command that lies to the platform.

## The key file

The bot's key is in the file at `$TNCTL_BOT_KEY_FILE`. `tnctl` re-reads it on every request and never
prints it. It is the bot's whole identity on the platform: anyone who copies it acts as this bot.

- Never read, print, copy, echo, encode or move it — not with `cat`, `head`, `base64`, `xxd`, `cp`,
  `grep`, `jq`, a redirect or a command substitution.
- Never pass it as an argument or an environment value of another program.
- The one exception is the `teknoirbot-app-key` helper of the `teknoir-mqtt-streams` skill: it copies
  the key into a Kubernetes Secret in the `apps` namespace so a pod there can read the platform
  stream. It never prints the value. Use it; do not do the job by hand.
- If a command says the key is missing or empty, the bot's computer predates the agent. A person must
  re-create the bot; you cannot fix it.

## Inside the computer

| Where | What it is |
|---|---|
| `apps` | Where you deploy what you build. Baseline pod security, and the agent may manage workloads there. |
| `teknoir-build` | The builder and the registry. The agent has no rights there. |
| `kube-system` | k3s, TOE and the local MQTT broker. The agent has no rights there. |
| `teknoir-desktop` | The desktop itself. Not yours to change. |

The builder, the registry and how to get an image from one to the other are the subject of the
`teknoir-k3s-apps` skill: read it before your first build. `buildctl` talks to `BUILDKIT_HOST`; a
service in `apps` pulls the image by `localhost:5000/<app>:<tag>`, never by the registry's service
name, and never by `:latest`.

The computer has a local MQTT broker with no authentication, at `mqtt.kube-system:1883`. TOE
republishes what apps publish there under `toe/events/<stream>` and `toe/state/<stream>` to the
teamspace's broker as `/devices/tb-<bot>/events/<stream>` and `…/state/<stream>`. The topic families,
the reserved names and the relay from the platform stream are in the `teknoir-mqtt-streams` skill.

Inside the computer you read the platform exactly as outside: `tnctl robots`, `tnctl config get`,
`tnctl inspect …`, `tnctl mqtt stream`. Nothing about bot mode changes those commands.

## The desktop

The desktop is a browser session into the computer. Who may open it:

| The bot's access | Who may open the desktop |
|---|---|
| Act as me (`creator`) | Only the bot's creator — the agent works with that person's rights. |
| Read only (`read`) | The creator, or a teamspace admin. |
| Off (`none`) | The creator, or a teamspace admin. |

Anyone else is refused with `only this TeknoirBot's creator and teamspace admins can open it`, or, at
Act as me, `this TeknoirBot acts as its creator; only its creator can open it`.

The desktop shows a banner: `TeknoirBot <bot> in <teamspace>, created by <creator>. Only its creator
(and teamspace admins, unless it acts as its creator) can open this computer. Never type your
Teknoir password here.` It is a reminder for the person in front of the browser, and it is also the
rule for you: this computer is shared with the person who opens it, keeps its home folder between
sessions, and is not a place for a Teknoir password or a `tnctl login`.

## The two modes of the agent's terminal

The agent starts in one of two modes, and the person can start the other from the desktop's menu:

- **The platform's model, as this bot.** The default. `tnctl` is in bot mode, with the bot's own key
  and its access level; the platform proxy supplies the model.
- **The person's own account, for one terminal.** The model is the person's own account, with a login
  that lives in memory and is removed when that terminal closes. The bot's key is not used for the
  model; `tnctl` still works as the bot.

Do not switch modes yourself, and do not sign in to anything on the bot's behalf. If a model is
unavailable, say so; a person chooses how to continue.

## What the bot cannot do, and who can

| The bot cannot | Who can |
|---|---|
| Remove, disable or enable a robot or a device | A teamspace admin |
| Read a device's login, open a tunnel, run `ssh`, `exec` or `ansible`, or `tnctl bootstrap` | A person with the developer role on a trusted workstation |
| Delete a robot type or a fleet | A person with write access to the whole teamspace |
| Manage bots, or change its own access level | The bot's creator (anyone who may edit the bot may lower it) |
| Reset every override of a robot at once | A person, in the portal |
| Change its teamspace | Nobody: a bot has one |
| Re-create itself when its key is gone | Its creator |

When one of these is in the way, print the exact command for the person to run and stop. Do not look
for a route, a flag or an environment variable that makes the bot do it.
