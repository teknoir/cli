This project manages Teknoir devices and robots.

- Use the `teknoir-*` Agent Skills for Teknoir work; start with `teknoir-platform`.
- `tnctl` is the CLI: `tnctl whoami`, `tnctl robots`, `tnctl inspect`, `tnctl config`.
- The `teknoir` MCP server (`tnctl mcp`) is read-only: it reads devices, robots and configuration.
- Change a robot with `tnctl config set`; the platform publishes the change to the robot.
- Never fetch or print device credentials, keys or passwords.

Installed by `tnctl agent install`; re-run it after a tnctl upgrade to update the kit.
