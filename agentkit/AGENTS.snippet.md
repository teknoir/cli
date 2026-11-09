<!-- teknoir-agent-kit:begin -->
This project manages Teknoir robots and devices with `tnctl`. Use the `teknoir-*` skills, `teknoir-platform` first; do not guess a command, read `tnctl <cmd> --help`.
Run `tnctl whoami -o json` first: `mode` is `user` (your own login) or `bot` (a TeknoirBot, limited rights — branch on `.bot.access`, not on error text).
Read with `tnctl robots|robot|devices|config get|config diff|inspect|mqtt stream`; before any change run `config diff` and the command's `--dry-run`, then the write, then `--wait=published`. Global flags go after the subcommand path.
The `teknoir` MCP server, if registered, is read-only; writes go through `tnctl`.
Never fetch, print or store device credentials or the bot key; never run `tnctl login` or type a Teknoir password in a bot's computer.
Read a robot's pods and logs with `tnctl inspect` and never with a raw inspect route through `tnctl api`, which turns the robot's Shell on.
Physical onboarding (`tnctl bootstrap`), `ssh`, `exec`, and pushing to Harbor or git are a person's steps: give them the exact command.
tnctl exits 6 when not allowed and 5 when the platform rejects a change; report them, do not work around them.
<!-- teknoir-agent-kit:end -->
