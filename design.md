# Report design

The design of the console report, static and interactive. Decided on 2026-09-23 with the owner. [ai-report.md](ai-report.md) has the product requirements. Its section "Next: report redesign" lists what the collector and the relay must add for this design.

Built on 2026-09-24 for v0.2.0. Where the build decides what this text leaves open, the golden files in `internal/view/testdata/` show the result: the matrix rows go by total, and OVER lines by when they run out.

## What the report answers

Top to bottom, in order of urgency:

1. **What needs attention now?** A subscription that is out or will run out, a device that fails.
2. **How much is left on each subscription?** How much, when it resets, and whether it will run out before that or be left partly unused.
3. **Who spends what?** Every device against every subscription.
4. **Where do this device's tokens go?** Its projects.

## Principles

- **Each fact once.** No note lines under rows. A detail that matters goes to ATTENTION; the rest is in `?` help and JSON.
- **Color means something.** It shows state or size, never decoration. Every colored thing also reads without color, as a word, a mark, or bold.
- **Numbers line up.** Right-aligned, units in the column header, not in the cell. Whole percents, whole millions of tokens, durations in at most two units.
- **Quiet by default.** Headers and secondary text are dim. Only state colors and large numbers stand out.
- **Whitespace, not boxes.** Sections and provider groups are separated by blank lines. The only rule is above the interactive key bar.
- **One page.** The static and the interactive report render the same page. The interactive one scrolls it.
- **Works at 80 columns.** It is at its best at 120 to 160. Columns drop in a fixed order as the width shrinks.

## The page

Real quota readings of the team on 23 September, 17:38. The token counts for 7 days are made up.

```
ai-usage · annbook · team qmvrtzpa                     ● collected 7m ago  ● relay 7m ago  ● up to date

ATTENTION
 OUT    codex ann@acme.dev          back Fri 17:09, in 1d 23h
 OUT    claude ann · Fable          back Sun 01:00, in 3d 7h
 OVER   codex sam@mail.test         runs out ~Sat 06:54 at this week's pace, 2d 13h before reset
 OVER   claude ann@acme.dev         runs out ~Thu 01:17 at this week's pace · reading 1d old
 ERROR  Mac.localdomain             codex: app-server exited without answering
 OLD    2 devices on v0.1.1         Mac.localdomain, MacBook-Pro-Kim · latest v0.1.3

SUBSCRIPTIONS  7 · 2 out · 1 over · 3 no reading

  CLAUDE                 PLAN        THIS WEEK                  LEFT  RESETS               AT RESET  USERS                LAST
● ann@acme.dev           max         ━━━━━━━━━╋━━━━━━────────   ~33%  3d 7h  Sun 01:00   ~174% over   2  annbook             7m
    Fable                            ━━━━━━━━━╋━━━━━━━━━━━━━━     0%  3d 7h  Sun 01:00          out
  kim@corp.test          max         ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈      ?                              —   1  Mac.localdomain     1h

  CODEX
● ann@acme.dev           pro         ━━━━━━━━━━━━━━━━━╋━━━━━━     0%  1d 23h Fri 17:09          out   2  annbook             1h
  sam@mail.test          prolite     ━━━━━━╋━━━──────────────    58%  5d 2h  Mon 20:12    157% over   1  MacBook-Pro-Kim     3h
● lee@corp.test          pro         ━━━─┃───────────────────    88%  5d 22h Tue 16:30       81% ok  10  srv1               34m
  unknown                            ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈      ?                              —   1  Mac.localdomain    16m

  GROK
● a4c2e917               SuperGrok…  ┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈┈      ?                              —   1  annbook            11h

DEVICES × SUBSCRIPTIONS  13 · 7d · M tokens in+out                        ‹tokens›  share
                      CLAUDE ─────────  CODEX ────────────────────────────  GROK     NO QUOTA
                          ann      kim      ann      sam      lee  unknown a4c2e917   hermes   TOTAL
● annbook                  19        ·      180        ·        ·        ·        3        ·     202
  srv1                      2        ·       12        ·      240        ·        ·        8     262
× Mac.localdomain           ·       10        ·        ·        ·       95        ·        ·     105
↓ MacBook-Pro-Kim           ·        ·        ·       60        ·        ·        ·        ·      60
  bot-a                     ·        ·        ·        ·       30        ·        ·        ·      30
  bot-b                     ·        ·        ·        ·       25        ·        ·        1      26
  ⋮
  TOTAL                    21       10      192       60      345       95        3       10     736

PROJECTS  annbook · by 7d · M tokens in+out
  PROJECT                             7D   90D  SESS  VIA            LAST
  ~/src/acme/app                      60   162    43  codex, claude    1h
  ~/Vault                             22    52    56  codex            3h
  ~/src/site                           9    30     6  codex            2d
  ~/src/ai-usage                       8     8     3  claude, grok     7m
  ~/src/acme/os                        3    10     3  claude, grok    11h
  + 237 more

 ↑↓ scroll · ←→ matrix · p period ‹7d› · % share · r refresh · ? help · q quit
```

## Header

One line: `ai-usage`, this device, the team, and on the right the collector's health: collection, relay, and update. Each item is a colored dot and a few words. A failure turns its dot red, and the error itself goes to ATTENTION.

## ATTENTION

Shown only when something is wrong. At most 6 lines, then `+N more`; the interactive view shows all. One line each, most urgent first:

| Badge | When | Says |
| --- | --- | --- |
| `OUT` | a window is at 100% | when it comes back |
| `OVER` | a window will run out before it resets, at its pace so far | when it runs out, and how long before the reset |
| `ERROR` | a device's collector or one of its harnesses fails | the device and the error |
| `SILENT` | a device has not reported for a day | since when, and the error it last reported, if any |
| `OLD` | devices run an older release | all of them in one line |
| `UNDER` | past half of a window, its forecast is under 50% | how much of the window will go unused |

A badge is its word in reverse video, colored by its state. `UNDER` is a hint rather than a problem: that subscription has room for more work.

## SUBSCRIPTIONS

Only accounts that have a subscription: Claude, Codex, and Grok. Hermes is a harness, not a subscription. What it spends through a Codex or Grok login counts as that login's use. Its accounts with no quota, such as API keys, appear only in the matrix, under NO QUOTA.

Each provider is a group, with a bold heading. Within a group, the worst state comes first: out, over, tight, ok, under, then no reading; ties go to the one with less left.

Columns:

| Column | Shows |
| --- | --- |
| mark | `●` when the account is logged in on this device |
| account | its label, or the short name the team gave it |
| PLAN | as the harness reports it, dim |
| THIS WEEK | the bar of the weekly window, described below |
| LEFT | what is left of the window, in percent |
| RESETS | the countdown and the local time of the reset |
| AT RESET | the forecast, described below |
| USERS | how many devices used the account in this window, and the busiest one |
| LAST | the newest activity on the account on any device |

A window other than the weekly one is a row of its own under the account. It is indented, has the same columns, and names only the window, such as `Fable` or `5h`. It is shown when it limits more than the weekly window: it is out, it is over, or it is fuller. A model window at 0% stays hidden.

An account with no reading has a dotted bar and `?` in LEFT. A reading older than 6 hours puts `~` before its numbers. A window that has reset since its reading shows `?`, since nobody knows how full the new one is.

### The bar

The bar is 24 cells wide, the whole window. The heavy line is what has been used. The tick marks how much would be used by now if the window were spent evenly. Past the tick, the account is spending faster than the window allows; short of it, slower.

```
out    ━━━━━━━━━━━━━━━━━╋━━━━━━    0%         out
over   ━━━━━━╋━━━──────────────   58%   157% over
tight  ━━━━━━━━━━━━━─┃─────────   45%   92% tight
ok     ━━━─┃───────────────────   88%      81% ok
under  ━━━━━─────────┃─────────   80%   33% under
━ used   ─ left   ┃ even use by now   ╋ the same tick inside the used part
```

The used part takes the color of the forecast state. The track is faint, and the tick is bright.

### The forecast

AT RESET is how full the window will be at its reset, if it is used from now on at its average pace so far. Over 100% means demand is larger than the quota: the window runs out before it resets.

- The window began at its reset time minus its length. The length comes from the harness, or from the window's name, such as `5h` or `7d`.
- `elapsed` is the share of the window that had passed when the reading was taken.
- The forecast is `used ÷ elapsed`, as a whole percent, up to `999%`.
- It runs out at `start + (100 ÷ used) × (reading time − start)`, when the forecast is over 100%.
- Before a tenth of the window has passed, the forecast is shown only when it is already over.
- The average since the window began includes nights and weekends, so one busy hour does not raise an alarm. It follows a change of pace slowly. Giving the last day more weight is later work.

States:

| State | Forecast | Color |
| --- | --- | --- |
| out | the window is at 100% | red |
| over | 100% or more | orange |
| tight | 85% to 99% | amber |
| ok | 50% to 84% | green |
| under | below 50% | blue |

The cell reads `157% over`, `81% ok`, or `out`. This replaces the 6-hour pace and its `▲` mark, the 75% and 90% marks, and `!!`.

### Users

USERS counts the devices with tokens on the account since the window began, and names the busiest. A Hermes bot in its own container is a device.

## DEVICES × SUBSCRIPTIONS

A matrix. Each device is a row, sorted by total, largest first. Each subscription is a column, grouped under its provider by a heading with a thin rule. The last column, NO QUOTA, holds tokens that have no subscription. Totals are on the right and at the bottom.

- A cell is input plus output tokens in the chosen period, in whole millions: `603` or `1210`. It is `<1` under a million and `·` with no use. Cache is left out.
- The cells form a heat map. A cell grows brighter, and at the top step bold, as its value grows, on a log scale relative to the largest cell. The largest consumers stand out without reading a number.
- A subscription's name in the header takes its state color, so an `out` column is visible from the matrix too.
- A mark before the device name gives its state: `●` this device, `×` an error, `~` silent, `↓` an old release. A silent device's error is the one it last reported, so it shows `~`.
- The share mode (`%`) shows each device's share of the subscription's current window instead. It is the device's tokens since the window began, divided by the team's, times how full the window is. A column then adds up to how full the window is, and the bottom row shows that. The title says the share is an estimate.

Many devices scroll down, and many subscriptions scroll sideways, with the device column and the headers kept in place. The static report prints the columns that fit and ends the header with `+N more`.

With a single device, a matrix of one row says little. The section becomes USAGE: a row per subscription, with today, 7d, 30d, and 90d.

## PROJECTS

One table for this device, all accounts together, sorted by the chosen period (7d by default). Columns: the project, the period, 90d, sessions, the providers it used, and the last activity. The static report shows the top 10 and says how many more there are. `--projects` prints all of them.

## Legend

The static report ends with one dim line of legend, listing only the marks on screen. The interactive view keeps the legend in `?` help.

## The interactive view

`ai-usage` opens it when standard input and output are both terminals. Piped output, `--json`, `--plain`, and a dumb terminal print the static report. So does the first run from the installer.

- It runs on the alternate screen, so the scrollback stays clean, and restores the terminal on exit, a crash included.
- The header stays at the top and the key bar at the bottom. The page scrolls between them.
- It reflows on resize. Relative times tick, and the page reloads when a scheduled run saves new state.

Keys:

| Key | Does |
| --- | --- |
| `↑` `↓` `j` `k`, `PgUp` `PgDn` `Space`, `g` `G`, the mouse wheel | scroll the page |
| `←` `→` `h` `l`, Shift and the wheel | scroll the matrix |
| `p`, or `1` `7` `3` `9` | the period: today, 7d, 30d, or 90d |
| `%` | tokens or share in the matrix |
| `r` | collect now; the header shows a spinner until it is done |
| `?` | every key and the legend, in a panel; `Esc` closes it |
| `q`, `Esc`, `Ctrl-C` | quit |

The key bar follows the common practice of modern TUIs:

- A key is bold, in the accent color. What it does is dim. Items are separated by a faint `·`.
- The current period and mode are pills: the chosen one in reverse accent, the others dim, as in `p period ‹7d›` and `‹tokens› share`.
- Only keys that do something now appear: `←→ matrix` only when the matrix does not fit.
- On a narrow terminal, the bar drops keys from the least used, and keeps `?` and `q` last.

## Visual system

### Color

Semantic tokens, never raw colors in the code. Each has a value for dark and for light terminals.

| Token | Use |
| --- | --- |
| text | the terminal's own foreground |
| muted | column headers, secondary text, units |
| faint | tracks, rules, dots, unknown bars |
| accent | keys, pills, and the this-device mark |
| out, over, tight, ok, under | forecast states and their badges |
| heat 1–5 | the matrix ramp, from faint to bright and bold |

With `--color never`, `NO_COLOR`, or a pipe, the words and marks carry the meaning. In the matrix, the largest value in each column is bold instead of the heat map.

### Glyphs

`--ascii` swaps each glyph for a plain one.

| Glyph | Meaning | ASCII |
| --- | --- | --- |
| `━` `─` `┃` `╋` | used, left, and even pace in a bar | `=` `-` `\|` `+` |
| `┈` | a window with no reading | `.` |
| `●` | this device, or logged in here | `*` |
| `×` `~` `↓` | error, silent or old reading, old release | `x` `~` `v` |
| `·` | nothing, and separators | `.` |
| `‹›` | the chosen option | `[]` |

### Text and numbers

- Section titles are bold capitals, followed by counts, with the alarming counts in their state color.
- Column headers are dim capitals.
- Durations: `7m`, `34m`, `5d 22h`, `1d 23h`. Clock times are local and 24-hour, with the weekday: `Fri 17:09`.
- Tokens: whole millions, `<1`, or `·`.
- Emails show in full in SUBSCRIPTIONS and are cut in the middle when they do not fit. The matrix uses short names.

### Width

The page is laid out for 120 to 160 columns, and down to 80. As SUBSCRIPTIONS narrows, it drops LAST, then PLAN, then the reset's clock time, then the busiest user's name, and last the bar shrinks from 24 cells to 12.

## Short names

- The matrix needs short account names. By default a name is the part of an email before the `@`, or the first 8 characters of an id. When two names collide within a provider, both show the full label.
- `ai-usage alias <account> <name>` names an account for the whole team, and `ai-usage alias <account> --clear` removes the name. The name travels sealed in the snapshot of the device that set it. If two devices set a name, the newer one wins.

## What goes

- The READ column; the reading's age shows only as `~`.
- USED BY as a list of names; the matrix shows who uses what.
- Every note line under a row: extra windows, pace, why a reading is old, "no reading yet", and the links between Hermes and Codex.
- The HERMES section.
- The `!!` and `▲` marks and the 6-hour pace in the console.
- THIS DEVICE's list of accounts; the matrix row and PROJECTS replace it.
- In DEVICES: VERSION, SEEN, IN+OUT, NOTE, and the `cl cx gk hm` grid. What was wrong with a device goes to ATTENTION.
- `--tokens` and `--devices`, which the matrix replaces.

## Build order

1. **Data.** Day buckets per account and project from the log timestamps. Tokens since each window began, per account and device. The larger snapshot and relay caps. JSON with the forecast of each window (percent, state, when it runs out) under a new schema version. The details are in [ai-report.md](ai-report.md).
2. **Forecast.** One function from a reading to its state, with tests for each state, stale readings, reset windows, and the first tenth of a window.
3. **Static report.** The page above on Lip Gloss, with golden files at 80, 120, and 160 columns, in color, and in ASCII.
4. **Interactive view.** On Bubble Tea, with the same renderer.
5. **Short names and `alias`.**
