# `tnctl council roster`

A council is the set of members that answers a chat with a TeknoirBot: a lead plans each turn, the members each do one
job, and a reviewer, when the council has one, checks the members' reports when the turn needs it. A council has a lead
and 1 to 5 members; the reviewer is required when the Coder is a member, and optional otherwise. The lead convenes at
most 3 members per turn.

A bot's default council is the one a new chat with the bot asks, and `tnctl council roster use` makes a council the
default. A bot may have at most 8 councils of its own; the premade councils below do not count. At that limit the
platform refuses a new one with `This bot has 8 councils. Delete one you no longer use.`

The platform ships three premade councils, which every bot's chats can ask. They are read-only:

| Council | Display name | Lead | Members | Reviewer | Budget |
| --- | --- | --- | --- | --- | --- |
| `standard` | Standard | `captain` | `fleet`, `coder` | `critic` | no council cap |
| `ops` | Ops | `captain` | `fleet`, `triage-sre`, `stream-analyst` | `critic` | no council cap |
| `review` | Review | `captain` | `architect`, `security-risk`, `edge-operator`, `ux-product`, `cost` | `skeptic` | 150000 tokens per question |

The budget column is the council's own budget; `roster get` prints `no council cap` for a council whose file has none,
and its turns then keep only the turn caps of [Budgets and deadlines](council.md#budgets-and-deadlines).

On a bot whose computer runs an older tnctl, only a council with the Standard council's premade lead, members and
reviewer and no budget can answer: Standard, or one of the bot's own made the same way
([`council`](council.md#what-it-does)). `roster list` shows the others, ops and review included, as `unavailable`,
and the platform refuses a turn on them with `This bot's computer runs an older council (tnctl <ver>) that knows
only the Standard council. Use the Standard council until its computer is updated.`

Every other council a bot's chats can ask is one of the bot's own: `tnctl council apply` writes one from a council file,
and `roster add` or `roster remove` with `--as` saves a copy of a council, premade or one of the bot's own, as a new
one.

The bot's default is one of its councils, its own or a premade one, unless the council it names is gone (see
[`roster list`](#roster-list)); `roster use` changes it, and so does a `--as` copy of a premade council that was the
default.

`tnctl council roster apply` is the same command as `tnctl council apply` (and `tnctl council role apply`); it applies a
council file, and [council-roles.md](council-roles.md) documents it.

## Synopsis

```bash
tnctl council roster list --bot <bot> [--json]
tnctl council roster get <name> --bot <bot> [-o json|yaml]
tnctl council roster use <name> --bot <bot> [-o json|yaml]
tnctl council roster add <name> <member> --bot <bot> [--scope <scope>] [--as <new-name>] [-o json|yaml]
tnctl council roster remove <name> <member> --bot <bot> [--scope <scope>] [--as <new-name>] [-o json|yaml]
tnctl council roster delete <name> --bot <bot>
```

## `roster list`

List the councils a bot's chats can ask: the premade ones first, then the bot's own by name. One row per council gives
its `NAME`, `DISPLAY NAME`, `SCOPE`, `LEAD`, `MEMBERS` and `REVIEWER`, the `DEFAULT` column (`yes` for the council a new
chat asks) and `STATUS`: `ok`, `unavailable` or `not checked` (the bot's computer has not yet told the platform which
tools it offers). In the table, the reason a council is unavailable follows on stderr, one line per council, and the
exit code stays 0. `--json` prints the councils as the platform sent them, as JSON, and none of the lines on stderr:
each council's reasons are in its `reasons` field. The global `-o` is ignored here: `--json` is this command's
machine-readable form.

Every council a bot has:

```bash
tnctl council roster list --bot scout
```

stdout:

```text
NAME       DISPLAY NAME  SCOPE     LEAD     MEMBERS                                                REVIEWER  DEFAULT  STATUS
standard   Standard      template  captain  fleet,coder                                            critic    -        ok
ops        Ops           template  captain  fleet,triage-sre,stream-analyst                        critic    -        ok
review     Review        template  captain  architect,security-risk,edge-operator,ux-product,cost  skeptic   -        ok
ops-scout  Ops scout     bot       captain  fleet,stream-watcher,cost-watcher                      critic    yes      ok
```

Nothing goes to stderr.

A council that cannot answer shows as `unavailable`, and stderr says why:

```bash
tnctl council roster list --bot scout
```

stdout:

```text
NAME       DISPLAY NAME  SCOPE     LEAD     MEMBERS                                                REVIEWER  DEFAULT  STATUS
standard   Standard      template  captain  fleet,coder                                            critic    -        ok
ops        Ops           template  captain  fleet,triage-sre,stream-analyst                        critic    -        ok
review     Review        template  captain  architect,security-risk,edge-operator,ux-product,cost  skeptic   -        ok
ops-scout  Ops scout     bot       captain  fleet,stream-watcher,cost-watcher,old-watch            critic    yes      unavailable
```

stderr:

```text
· ops-scout: old-watch: this member was archived or deleted; replace it in this council.
```

When the bot's default council is gone, no row has `yes` under `DEFAULT`, and a hint on stderr names the command that
makes another council the default:

```bash
tnctl council roster list --bot scout
```

stdout:

```text
NAME       DISPLAY NAME  SCOPE     LEAD     MEMBERS                                                REVIEWER  DEFAULT  STATUS
standard   Standard      template  captain  fleet,coder                                            critic    -        ok
ops        Ops           template  captain  fleet,triage-sre,stream-analyst                        critic    -        ok
review     Review        template  captain  architect,security-risk,edge-operator,ux-product,cost  skeptic   -        ok
ops-scout  Ops scout     bot       captain  fleet,stream-watcher,cost-watcher                      critic    -        ok
```

stderr:

```text
· scout's default council is gone; tnctl council roster use <council> --bot scout makes another one the default
```

No tnctl command or web chat change leaves that state: deleting the default council is refused (`roster_default`, see
[`roster delete`](#roster-delete)), and `roster use` takes only a council that exists. A stored default the platform
cannot find is what a platform upgrade that dropped a premade council would leave. When you may not change the bot's
council, the hint says the bot's creator or a teamspace admin makes another one the default instead.

When the bot has no council of its own and you may change its council, a hint on stderr names the command that saves a
copy of a premade council with one more member (anyone else gets the same table and no hint):

```bash
tnctl council roster list --bot scout
```

stdout:

```text
NAME      DISPLAY NAME  SCOPE     LEAD     MEMBERS                                                REVIEWER  DEFAULT  STATUS
standard  Standard      template  captain  fleet,coder                                            critic    yes      ok
ops       Ops           template  captain  fleet,triage-sre,stream-analyst                        critic    -        ok
review    Review        template  captain  architect,security-risk,edge-operator,ux-product,cost  skeptic   -        ok
```

stderr:

```text
· scout has no council of its own yet; tnctl council roster add <council> <member> --bot scout --as scout-council saves a copy with that member
```

An argument is refused before any request, exit 2: `council roster list takes no arguments; name the bot with --bot, for
example: tnctl council roster list --bot scout`.

## `roster get`

Show one council: its status and budget, then one row each for the lead, the members in order and the reviewer, with
each one's scope, version (followed by `(pinned)` when the council asks for that version), model, effort and status. A
member that was archived or deleted shows as `gone`.

`-o json` prints the council as the platform sent it. `-o yaml` prints its council file, as the platform exports it. A
council's file can be edited and applied with `tnctl council apply`; a premade council's file needs a new
`metadata.name`, `spec.scope: bot` and `spec.bot: <bot>`, and no `spec.version`, before it can be applied.

A council of the bot's own, with a pinned member and a gone one:

```bash
tnctl council roster get ops-scout --bot scout
```

stdout:

```text
council:     Ops scout (ops-scout)
scope:       bot
version:     3
default:     yes
status:      unavailable: old-watch: this member was archived or deleted; replace it in this council.
budget:      200000 tokens per question
description: Day-to-day operations for scout: the facts, the MQTT streams and what the fleet costs, checked by the Critic.

PART      SLUG            NAME            SCOPE      VERSION     MODEL   EFFORT  STATUS
lead      captain         Captain         template   1           sonnet  high    ok
member    fleet           Fleet           template   1           haiku   medium  ok
member    stream-watcher  Stream watcher  bot        1           haiku   medium  ok
member    cost-watcher    Cost watcher    teamspace  2 (pinned)  haiku   medium  ok
member    old-watch       -               bot        -           -       -       gone
reviewer  critic          Critic          template   1           sonnet  medium  ok
```

Nothing goes to stderr.

A premade council, with a budget:

```bash
tnctl council roster get review --bot scout
```

stdout:

```text
council:     Review (review)
scope:       template
version:     1
default:     no
status:      ok
budget:      150000 tokens per question
description: Reviews a plan from five angles (architecture, security, edge operations, UX and cost); the Skeptic argues against them.

PART      SLUG           NAME               SCOPE     VERSION  MODEL   EFFORT  STATUS
lead      captain        Captain            template  1        sonnet  high    ok
member    architect      Architect          template  1        sonnet  medium  ok
member    security-risk  Security and Risk  template  1        sonnet  medium  ok
member    edge-operator  Edge Operator      template  1        sonnet  medium  ok
member    ux-product     UX and Product     template  1        sonnet  medium  ok
member    cost           Cost               template  1        haiku   medium  ok
reviewer  skeptic        Skeptic            template  1        sonnet  medium  ok
```

Nothing goes to stderr.

A premade council with no budget of its own:

```bash
tnctl council roster get standard --bot scout
```

stdout:

```text
council:     Standard (standard)
scope:       template
version:     1
default:     no
status:      ok
budget:      no council cap
description: Fleet finds the facts and the Coder writes code; the Critic checks their reports.

PART      SLUG     NAME     SCOPE     VERSION  MODEL   EFFORT  STATUS
lead      captain  Captain  template  1        sonnet  high    ok
member    fleet    Fleet    template  1        haiku   medium  ok
member    coder    Coder    template  1        sonnet  high    ok
reviewer  critic   Critic   template  1        sonnet  medium  ok
```

Nothing goes to stderr.

A council the bot does not have exits 2, and the second line lists the bot's councils:

```bash
tnctl council roster get nope --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: There is no council 'nope' for scout.
councils: standard, ops, review, ops-scout
Run the command with --help for usage and examples.
```

The council file of ops-scout while it names no gone member, to edit and apply:

```bash
tnctl council roster get ops-scout --bot scout -o yaml
```

stdout:

```text
apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-scout
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Ops scout
  description: "Day-to-day operations for scout: the facts, the MQTT streams and what the fleet costs, checked by the Critic."
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
    - role: stream-watcher
      scope: bot
    - role: cost-watcher
      scope: teamspace
      version: 2
  reviewer:
    role: critic
    scope: template
  budget:
    maxTokens: 200000
```

Nothing goes to stderr.

When the council names a gone member by its slug, as ops-scout names `old-watch` in the first `roster get` sample above,
its file still lists that member. `roster remove` reads this file, takes the member out and saves the council again,
which is the repair (see [A council that names a gone member](#a-council-that-names-a-gone-member)). When the council
names a lead, member or reviewer shown only by its `rol_…` id, the platform refuses its export (`role_gone`):
`roster get -o yaml` prints nothing on stdout and exits 1, with
`This council names a member that no longer exists; replace it before you export the council.` and then
`fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>` on stderr. The file
of ops-scout, which names `old-watch`:

```bash
tnctl council roster get ops-scout --bot scout -o yaml
```

stdout:

```text
apiVersion: council.teknoir.org/v1alpha1
kind: CouncilRoster
metadata:
  name: ops-scout
  namespace: teknoir-demo
spec:
  scope: bot
  bot: scout
  displayName: Ops scout
  description: "Day-to-day operations for scout: the facts, the MQTT streams and what the fleet costs, checked by the Critic."
  lead:
    role: captain
    scope: template
  members:
    - role: fleet
      scope: template
    - role: stream-watcher
      scope: bot
    - role: cost-watcher
      scope: teamspace
      version: 2
    - role: old-watch
      scope: bot
  reviewer:
    role: critic
    scope: template
  budget:
    maxTokens: 200000
```

Nothing goes to stderr.

## `roster use`

Make a council the bot's default: the one a new chat asks, unless it names another (`tnctl chat --roster`). Only the
bot's creator or a teamspace admin may change it, among those who may chat with the bot; anyone else who may see the
bot is refused with `council_edit_not_allowed`, exit 6 (see [Who may](#who-may)). `-o json|yaml` prints the bot's
council as the platform answers.

```bash
tnctl council roster use ops --bot scout
```

stdout:

```text
scout's default council is now Ops (ops)
```

Nothing goes to stderr.

The platform takes an unavailable council as the default; tnctl prints its status and reason on stderr, and the exit
code stays 0:

```bash
tnctl council roster use ops-scout --bot scout
```

stdout:

```text
scout's default council is now Ops scout (ops-scout)
```

stderr:

```text
· council ops-scout — unavailable: old-watch: this member was archived or deleted; replace it in this council.
```

A council the bot does not have exits 2; unlike `roster get`, the second line names the command that lists the bot's
councils instead of listing them:

```bash
tnctl council roster use nope --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: There is no council 'nope' for scout.
tnctl council roster list --bot scout lists its councils
Run the command with --help for usage and examples.
```

Someone other than anna@example.com, even a teamspace admin: scout acts as its creator (Act as me), so only
anna@example.com may change its council:

```bash
tnctl council roster use ops --bot scout
```

Nothing goes to stdout. stderr, exit 6:

```text
Error: Only anna@example.com can open this computer: its council acts as them.
```

## `roster add` and `roster remove`

Add a member to a council, after its members, or take one out. A council keeps at least one member, and the platform
checks the changed council. When it saves a council that cannot answer, stderr adds
`· council <name> — unavailable: <reasons>`, as `roster use` does. `-o json|yaml` prints the council as the platform
answers, on stdout; the line that says a
copy became the bot's default then goes to stderr. `<member>` is the member's slug, such as `cost-watcher`; when more
than one member has that slug, name its scope too: `<scope>/<slug>`, or the slug with
`--scope bot|teamspace|template`. `tnctl council role list --bot <bot> --templates` lists the members a bot can use,
with their slugs and scopes.

A council the bot does not have exits 2, and, as with `roster get`, a second line `councils: …` lists the bot's
councils.

The samples below do not follow on from each other: each one starts from its own data, so the versions and members they
print do not add up from one sample to the next.

### `--as`, and the derived name

A premade council cannot be changed: `--as <new-name>` saves a copy with the change, and when the premade council was the
bot's default, the copy becomes the default. On one of the bot's own councils, `--as` saves a copy and leaves the council
as it was. When the platform saves the copy but refuses to make it the default, the command exits with that refusal's
code, and stderr adds after the refusal: `council <new-name> was saved, but it is not the default: tnctl council
roster use <new-name> --bot <bot> makes it the default`.

A premade council that was the default, copied with a new member: the copy becomes the default too:

```bash
tnctl council roster add standard stream-watcher --bot scout --as scout-council
```

stdout:

```text
created council scout-council (version 1) for scout, a copy of standard; members: fleet, coder, stream-watcher
scout's default council is now scout-council
```

Nothing goes to stderr.

A premade council without `--as` is refused, exit 2:

```bash
tnctl council roster add standard stream-watcher --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: 'standard' is a premade council and cannot be changed: use --as scout-council to save a copy
Run the command with --help for usage and examples.
```

A taken `--as` name is refused, exit 2, after tnctl reads the bot's councils and before it changes anything. The name is
taken when it is the council being changed, another of the bot's own councils, or a premade name. `--as standard` on
standard itself gets the premade refusal above.

`--as` names the council being changed:

```bash
tnctl council roster add ops-scout analyst --bot scout --as ops-scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: --as ops-scout names the council you are changing: leave out --as to change it (tnctl council roster add ops-scout analyst --bot scout), or give the copy a new name, such as scout-council
Run the command with --help for usage and examples.
```

`--as` names another council of the bot's own:

```bash
tnctl council roster add standard analyst --bot scout --as ops-scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: there is a council 'ops-scout' for scout already: to change it, tnctl council roster add ops-scout analyst --bot scout; to save another copy, pick another name with --as, such as scout-council
Run the command with --help for usage and examples.
```

`--as` is a premade name:

```bash
tnctl council roster add ops-scout analyst --bot scout --as review
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: there is a council 'review' for scout already: pick another name with --as, such as scout-council
Run the command with --help for usage and examples.
```

The name those refusals suggest, and the one the `roster list` hint for a bot with no council of its own suggests, is
derived from the bot's name: `<bot>-council`, with `c-` in front when the bot's name starts with a digit, cut to 24
bytes with trailing dashes trimmed after every cut; while that name is taken, `-2`, `-3` … with the base cut so that
the whole name stays within 24. A premade name counts as taken, and so does a cut that is not a valid council name. So
`scout` gives `scout-council`, then `scout-council-2` and `scout-council-3`; `7up` gives `c-7up-council`;
`abcdefghijklmnopqrstuvwxyz1234` gives `abcdefghijklmnopqrstuvwx`, then `abcdefghijklmnopqrstuv-2`.

### A member the council has already

`roster add` of a member the council has exits 0 and changes nothing, once the checks that come first pass: you may
change the council (exit 6 otherwise), a premade council needs `--as` and the `--as` name must be free (exit 2, above),
the council names no gone member (exit 1, [below](#a-council-that-names-a-gone-member)), and a scope you give is the
member's (otherwise exit 2, `<council> has a member '<slug>' already (<scope>/<slug>): member slugs are unique in a
council`):

```bash
tnctl council roster add ops-scout stream-watcher --bot scout
```

Nothing goes to stdout. stderr:

```text
· stream-watcher is already a member of ops-scout: nothing to change
```

With `--as`, the note adds that no copy was saved either:

```bash
tnctl council roster add ops-scout stream-watcher --bot scout --as ops-copy
```

Nothing goes to stdout. stderr:

```text
· stream-watcher is already a member of ops-scout: nothing to change, so ops-copy was not saved
```

### A council that names a gone member

An archived member is still written in a council's export, so the council can be fixed. tnctl reads the bot's councils,
and when the council names a member that was archived or deleted — as its lead, a member or its reviewer — it refuses
the change before it reads the council's file or changes anything, exit 1. The first line names the part:
`council <c> names a member that was archived or deleted: <slug>`, and for the lead or the reviewer it is
`council <c> has a lead that was archived or deleted: <slug>`, or `… has a reviewer …`.

A gone member is never the result of an archive: the platform refuses to archive a member that a council names
([`council role archive`](council-roles.md#council-role-archive)), and refuses a council write or apply that names an
archived member (`unknown_member`). No tnctl command or web chat change leaves one; only data they did not write does:
a restore of old data, a repair, or a premade member a platform upgrade dropped.

When the council is one of the bot's own, and that gone member is a member shown by its slug (not only by its `rol_…`
id) and is the council's only gone reference (the lead, the members and the reviewer counted), the second line is the
remove that fixes it. Otherwise (two or more gone references, a gone lead or reviewer, a gone member shown only by its
id, or a premade council changed with `--as`) the second line is
`fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>`.

`roster remove` of the gone member itself (named with its own scope, or with none) is not refused when it is the
council's only gone reference: it is the repair. When the council has another gone reference (a second member, the lead
or the reviewer), that remove is refused too, exit 1: the first line names the other one (the lead first, then the
members, then the reviewer), and the second line is
`fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>`.

The council's only gone member:

```bash
tnctl council roster add ops-scout analyst --bot scout
```

Nothing goes to stdout. stderr, exit 1:

```text
Error: council ops-scout names a member that was archived or deleted: old-watch
take it out first: tnctl council roster remove ops-scout old-watch --bot scout
```

When the council also names a second gone member, `probe-x`, the first line still names only the first one,
`old-watch`. Taking one out is not enough, so the second line names the web chat's Council tab or apply:

```bash
tnctl council roster add ops-scout analyst --bot scout
```

Nothing goes to stdout. stderr, exit 1:

```text
Error: council ops-scout names a member that was archived or deleted: old-watch
fix it in the web chat's Council tab, or write the council again with tnctl council apply -f <file>
```

### `roster add`

Exit codes, as its help gives them: 0 done, or nothing to change; 1 the council names a member that was archived or
deleted; 2 wrong arguments, an unknown council or member, a slug more than one member has, a premade council without
`--as`, or an `--as` name that is taken; 3 no bot of that name; 5 the platform refused the changed council; 6 not
allowed; 8 someone changed the council meanwhile: run it again.

A member added to a council of the bot's own:

```bash
tnctl council roster add ops-scout analyst --bot scout
```

stdout:

```text
updated council ops-scout (version 4) for scout; members: fleet, stream-watcher, cost-watcher, analyst
```

Nothing goes to stderr.

A member the bot cannot use is refused by the platform, exit 2:

```bash
tnctl council roster add ops-scout nightwatch --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: spec.members[3]: 'nightwatch' is not a member this bot can use.
tnctl council role list --bot scout --templates lists the members scout can use
Run the command with --help for usage and examples.
```

Someone changed the council meanwhile, exit 8:

```bash
tnctl council roster add ops-scout analyst --bot scout
```

Nothing goes to stdout. stderr, exit 8:

```text
Error: anna@example.com changed this council at 12:52 UTC (version 4). Reload to see it, then make your change again.
```

Someone other than anna@example.com, even a teamspace admin (scout acts as its creator):

```bash
tnctl council roster add ops-scout analyst --bot scout
```

Nothing goes to stdout. stderr, exit 6:

```text
Error: Only anna@example.com can open this computer: its council acts as them.
```

### `roster remove`

Exit codes, as its help gives them: 0 done; 1 the council names another member that was archived or deleted; 2 wrong
arguments, an unknown council, a member that is not in it, a premade council without `--as`, or an `--as` name that is
taken; 3 no bot of that name; 5 the platform refused the changed council; 6 not allowed; 8 someone changed the council
meanwhile: run it again.

A member taken out of a council of the bot's own:

```bash
tnctl council roster remove ops-scout stream-watcher --bot scout
```

stdout:

```text
updated council ops-scout (version 4) for scout; members: fleet, cost-watcher
```

Nothing goes to stderr.

A member the council does not have is a usage error, and the refusal names the members it has, exit 2:

```bash
tnctl council roster remove ops-scout coder --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: 'coder' is not a member of ops-scout. Members: fleet, stream-watcher, cost-watcher.
Run the command with --help for usage and examples.
```

The lead and the reviewer are not members, so `roster remove` refuses them. The reviewer is changed in the web chat's
Council tab or in YAML (`tnctl council apply`), and the lead in YAML for now.

The lead:

```bash
tnctl council roster remove ops-scout captain --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: 'captain' leads ops-scout; it is not a member: the lead is changed in YAML (tnctl council apply) for now
Run the command with --help for usage and examples.
```

The reviewer:

```bash
tnctl council roster remove ops-scout critic --bot scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: 'critic' is the reviewer of ops-scout, not a member: change the reviewer in the web chat's Council tab or in YAML (tnctl council apply)
Run the command with --help for usage and examples.
```

Taking the gone `old-watch` out repairs the council when it is the only gone member, as in the first refused `roster add`
above:

```bash
tnctl council roster remove ops-scout old-watch --bot scout
```

stdout:

```text
updated council ops-scout (version 4) for scout; members: fleet, stream-watcher, cost-watcher
```

Nothing goes to stderr.

## `roster delete`

Delete one of a bot's own councils. The chats that asked the deleted council ask the bot's default council from then
on. tnctl asks nothing: to keep a copy, save it first with
`tnctl council roster get <name> --bot <bot> -o yaml > <name>.yaml`, and `tnctl council apply -f <name>.yaml` makes it
again. Someone who may see the bot but may not change its council is refused before the default and premade checks
below (`council_edit_not_allowed`, exit 6; [Who may](#who-may)).

ops-scout can be deleted once another council is scout's default (here after
`tnctl council roster use ops --bot scout`):

```bash
tnctl council roster delete ops-scout --bot scout
```

stdout:

```text
deleted council ops-scout of scout
```

stderr:

```text
· chats that asked it now ask scout's default council
```

The default council cannot be deleted; make another one the default first (`roster_default`, exit 5), and the hint names
the command that does it:

```bash
tnctl council roster delete ops-scout --bot scout
```

Nothing goes to stdout. stderr, exit 5:

```text
Error: This is scout's default council. Make another one the default first.
tnctl council roster use <council> --bot scout makes another council the default
```

A premade council cannot be deleted (`template_read_only`, exit 5):

```bash
tnctl council roster delete standard --bot scout
```

Nothing goes to stdout. stderr, exit 5:

```text
Error: Premade members and councils are read-only: give it a new name to clone it.
```

Without `--bot`, exit 2:

```bash
tnctl council roster delete ops-scout
```

Nothing goes to stdout. stderr, exit 2:

```text
Error: council roster delete needs --bot <bot>: a council belongs to one bot, for example: tnctl council roster delete ops-scout --bot scout
Run the command with --help for usage and examples.
```

## Who may

| What | Who |
| --- | --- |
| List a bot's councils and read one (`roster list`, `roster get`), and the file of a premade council (`roster get -o yaml`) | Any device role — viewer, developer or admin — and the bot in your organisation units. |
| The file of one of the bot's own councils (`roster get <name> -o yaml`) | Whoever may chat with the bot; anyone else who may see the bot is refused with `chat_not_allowed`, exit 6. |
| Change a council or the bot's default (`roster use`, `roster add`, `roster remove`, `roster delete`, and `tnctl council apply`) | The bot's creator or a teamspace admin, among those who may chat with the bot. |

For someone who may see the bot, a change is refused with `council_edit_not_allowed`, exit 6, and its text is the
platform's reason why you may not drive the bot, for example at Act as me:
`Only anna@example.com can open this computer: its council acts as them.`

Inside a TeknoirBot, tnctl refuses every `tnctl council roster …` before any request, exit 6, with
`"tnctl council roster list" is not available to a TeknoirBot: only a person reads and changes a bot's council.` The
quoted command is the command's path, without its arguments and flags. Their `--help` still prints. A mistyped
subcommand, a missing or extra argument, an unknown flag or a flag value of the wrong type is a usage error first
(exit 2); other wrong flags, such as `roster list` without `--bot`, get the exit-6 refusal
([Bot mode](../bot-mode.md)).

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | Done: a council listed, read, made the default, changed or deleted; or `roster add` of a member the council already has, which changes nothing (unless a refusal comes first, such as a council that names a gone member: exit 1). |
| 1 | `roster add`: the council names a member that was archived or deleted (as its lead, a member or its reviewer); `roster remove`: it names one other than the member taken out; `roster get <name> -o yaml`: the council names a lead, member or reviewer shown only by its `rol_…` id (`role_gone`). |
| 2 | Wrong arguments: an unknown council or member, a member that is not in the council, a slug more than one member has, a premade council without `--as`, an `--as` name that is taken, or a missing `--bot`. |
| 3 | No bot of that name, or a bot outside your organisation units (the platform answers as if it did not exist). |
| 5 | `roster delete`: the platform refused it, `roster_default` (the council is the bot's default) or `template_read_only` (it is a premade council); `roster add` or `roster remove`: the platform refused the changed council. |
| 6 | Not allowed: a change refused with `council_edit_not_allowed`; `roster get <name> -o yaml` of one of the bot's own councils by someone who may not chat with the bot (`chat_not_allowed`); a `roster` command by someone without a device role in the teamspace; and, inside a TeknoirBot, every `tnctl council roster …` (`--help` prints, exit 0; a mistyped subcommand, a missing or extra argument, an unknown flag or a flag value of the wrong type is exit 2 first; other wrong flags, such as `roster list` without `--bot`, are refused too). |
| 8 | `roster add` or `roster remove`: someone changed the council meanwhile; run it again. |

## See also

- [`council role`](council-roles.md) - the members a council may use, and `council apply`.
- [`chat`](chat.md) - chat with a bot's council, and ask one member alone.
- [`council`](council.md) - the council that runs on a bot's computer.
