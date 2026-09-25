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
 OVER   claude ann@acme.dev         runs out ~Thu 01:17 at this week's pace, 2d 23h before reset · reading 1d old
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

DEVICES × SUBSCRIPTIONS  13 · 7d · M tokens in+out      ‹usage›  status   ‹tokens›  share
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

 ↑↓ scroll · ←→ matrix · s ‹usage› status · p period ‹7d› · % share · r refresh · ? help · q quit
```

## Header

One line: `ai-usage`, this device, the team, and on the right the collector's health: collection, relay, and update. Each item is a colored dot and a few words. A failure turns its dot red, and the error itself goes to ATTENTION.

## ATTENTION

Shown only when something is wrong. At most 6 lines, then `+N more`; the interactive view shows all. One line each, most urgent first:

| Badge | When | Says |
| --- | --- | --- |
| `OUT` | a window is at 100% | when it comes back |
| `OVER` | a window will run out by its reset, at its pace so far | when it runs out, and how long before the reset |
| `ERROR` | a device's collector or one of its harnesses fails | the device and the error |
| `SILENT` | a device has not reported for a day | since when, and the error it last reported, if any |
| `OLD` | devices run an older release | all of them in one line |
| `UNDER` | past half of an account's weekly window, its forecast is under 50% | how much of the window will go unused |

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
- It runs out at `start + (100 ÷ used) × (reading time − start)`, when the forecast is over 100% before it is rounded. At exactly 100% it runs out at its reset.
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

A device on a collector older than v0.2.0 does not send its tokens since the window began, so it counts when it used the account since then. The busiest is the one with the most tokens among the devices whose tokens are known. When the only user is such a device, it is named all the same; when every one of two or more is, none is named.

## DEVICES × SUBSCRIPTIONS

A matrix. Each device is a row, sorted by total, largest first. Each subscription is a column, grouped under its provider by a heading with a thin rule. The last column, NO QUOTA, holds tokens that have no subscription. Totals are on the right and at the bottom.

- A cell is input plus output tokens in the chosen period, in whole millions: `603` or `1210`. It is `<1` under a million and `·` with no use. Cache is left out.
- The cells form a heat map. A cell grows brighter, and at the top step bold, as its value grows, on a log scale relative to the largest cell. The largest consumers stand out without reading a number.
- A subscription's name in the header takes its state color, so an `out` column is visible from the matrix too.
- A mark before the device name gives its state: `●` this device, `×` an error, `~` silent, `↓` an old release. A silent device's error is the one it last reported, so it shows `~`.
- The share mode (`%`) shows each value as a percent of its column's total in the chosen period instead: a device's tokens on a subscription divided by the team's, so a column adds up to 100 and the bottom row shows that. TOTAL on the right is the device's part of all the team's tokens. It is how the tokens split, not how much of a quota the device used: SUBSCRIPTIONS shows how full each window is. The cells round to whole percents, `<1` under one and `>99` over 99 but under 100.
- A device on a collector older than v0.2.0 sends each account's tokens over 90 days, but no days and no tokens since a window began. Its 90d cell is those tokens, the report's 90 days as nearly as that collector counts them: they end when the device last collected, days before the report's day if it has been silent, and hold a session whole while it was active in them. A shorter period is `·` when the account was last active on the device before the period began, and `?` otherwise: it is not known, not 0. Such a row sorts by what is known, before the rows with none.
- A total with a `?` in it never reads as exact. It is `≥` before the part that is known, in whole millions rounded down so the bound holds, as `≥68`, or `?` when that part is under a million. The rule is the same for a row's total, a column's, and the grand total, in every period.
- In the share mode, a column whose tokens in the period are not all known cannot be split. The share of such a device is `?`, and so is the share of every other device that used the subscription in the period; a device that did not is `·`. The bottom row is still 100 when the known part is above 0, and `?` when it is not.

Many devices scroll down, and many subscriptions scroll sideways, with the device column and the headers kept in place: the interactive view keeps the title, the providers' line, and the subscriptions' names at the top of the page while the rows scroll under them. The static report prints the columns that fit and ends the header with `+N more`.

The section has two views, usage and status. The title names them as pills before the matrix's modes, `‹usage›  status   ‹tokens›  share`, where both pairs fit; where they do not, as at 80 columns, the views go and the modes stay.

With a single device, a matrix of one row says little. The section becomes USAGE: a row per subscription, with today, 7d, 30d, and 90d. It has one view, so `s` and `--devices` change nothing there.

### Status

The status view is DEVICES as a table of each device's state, as the DEVICES table before v0.2.0 had it: which devices report, on which release, and what fails on them. `s` in the interactive view switches to it and back; `--devices` prints it and opens the interactive view on it. It came back on 2026-09-24 at the owner's request.

```
DEVICES  13 · 1 error · 2 old · by 7d · M tokens in+out                                                 usage  ‹status›
                           COLLECTOR ───────────────────────────  TOKENS ────────────────
  DEVICE           USER    VERSION   SEEN  VIA                    TODAY    7D   30D   90D  NOTE
  srv1             root    v1.4.2      8m  claude, codex, hermes     31   262   786  2227
● annbook          ann     v1.4.2      7m  claude, codex, grok       42   202   849  2224
× Mac.localdomain  kim     v1.4.0 ↓   16m  claude, codex ×           32   105   400   947  codex: app-server exited wi…
↓ MacBook-Pro-Kim  sam     v1.4.0 ↓   30m  codex                      3    60   149   239  update: latest v1.4.2
  bot-a            hermes  v1.4.2      9m  codex, hermes              4    30   105   210
  ⋮
  TOTAL                                                             123   736  2559  6387
```

- The rows are the matrix's, in the matrix's order for the chosen period, and the section is as tall as the matrix: a title, a line of group headings, the column headers, the rows, and TOTAL. A device keeps its line when the view changes, so the page keeps its place. The interactive view keeps its first three lines at the top while the rows scroll, as it keeps the matrix's.
- The title counts the devices that fail, are silent, or run an older release than the team's newest, each in its state's color: errors in the out color, silent in the tight color, old dim. A count of none is left out. A silent device counts as silent, not as an error, since its error is the one it last reported.
- COLLECTOR heads VERSION, SEEN, and VIA, and TOKENS the periods, each with a thin rule, as the providers head the matrix's columns.
- The status view has no share mode. `%` leaves the key bar and does nothing there, and the matrix keeps its mode and its sideways scroll for when it shows again.

| Column | Shows |
| --- | --- |
| mark | as in the matrix: `●` this device, `×` an error, `~` silent, `↓` an old release |
| DEVICE | the matrix's name for it |
| USER | the OS user, dim |
| VERSION | the collector's release; an older one than the team's newest is dim and followed by `↓` |
| SEEN | how long ago it last reported, dim; a silent device's in the tight color |
| VIA | the harnesses it reads, dim; one that fails or reads only in part is in the out color and followed by `×`; `·` for none |
| TODAY 7D 30D 90D | its input plus output tokens, as the matrix's TOTAL column prints them, with `≥` and `?` for a device on a collector older than v0.2.0; their headers are dim, as every header is, and the title names the chosen period, as in `by 7d` |
| NOTE | for a silent device, `silent since` when it last reported, then the error it last reported, as ATTENTION says it; else what fails on it, in the out color; else `update: latest v1.4.2`; else nothing. Only the error is in color |

The bottom row is TOTAL for each period, by the matrix's rules for `≥` and `?`. With no note on any device, there is no NOTE column.

A short NOTE cuts an error with `…`, and keeps a silent device's last error only while 12 columns of it fit. It never cuts a time: a silent device's note becomes `since Mon 14:02`, then the day alone, as in `since Mon`, then `silent`. An old release's becomes `latest v1.4.2`.

## PROJECTS

One table for this device, all accounts together, sorted by the chosen period (7d by default). Columns: the project, the period, 90d, sessions, the providers it used, and the last activity. The static report shows the top 10 and says how many more there are. `--projects` prints all of them.

## Legend

The static report ends with a dim legend, listing only the marks on screen, a word or two each:

```
━ used  ─ left  ┃╋ even use  ┈ no reading  ~ stale  ? unknown  — no forecast  ● here  × error  ↓ old  · none  ‹› chosen
```

With every mark on screen it is one line from 120 columns on. `≥ at least`, which only a device on a collector older than v0.2.0 brings, follows `? unknown`; with it too, the line needs 133 columns. Below that, where one line does not fit, it takes as few lines as it can, of even length: two at 80 columns. The interactive view keeps the legend in `?` help, which says more of each mark.

## The interactive view

`ai-usage` opens it when standard input and output are both terminals. Piped output, `--json`, `--plain`, and a dumb terminal print the static report. So does the first run from the installer.

- It runs on the alternate screen, so the scrollback stays clean, and restores the terminal on exit, a crash included.
- The header stays at the top and the key bar at the bottom. The page scrolls between them.
- Scrolled into DEVICES, the page keeps the section's three lines over its rows at its top, in either view, as a sticky table header. It stays from the line its title would scroll off on, with the first row right under it the line before, until TOTAL comes up under it, and then lets go. A screen down starts under it with the line after the last one shown, so no row is skipped.
- It reflows on resize. Relative times tick, and the page reloads when a scheduled run saves new state.

Keys:

| Key | Does |
| --- | --- |
| `↑` `↓` `j` `k`, `PgUp` `PgDn` `Space`, `g` `G`, the mouse wheel | scroll the page |
| `←` `→` `h` `l`, Shift and the wheel | scroll the matrix |
| `s` | DEVICES as the matrix or as each device's status |
| `p`, or `1` `7` `3` `9` | the period: today, 7d, 30d, or 90d, in either view |
| `%` | tokens or share in the matrix |
| `r` | collect now; the header shows a spinner until it is done |
| `?` | every key and the legend, in a panel; `Esc` closes it |
| `q`, `Esc`, `Ctrl-C` | quit; a collection `r` started stops, and what the stop cuts short is not saved as a failure |

The key bar follows the common practice of modern TUIs:

- A key is bold, in the accent color. What it does is dim. Items are separated by a faint `·`.
- The current period and mode are pills: the chosen one in reverse accent, the others dim, as in `p period ‹7d›` and `‹tokens› share`. The views of DEVICES are pills after their key, as in `s ‹usage› status`.
- Only keys that do something now appear: `←→ matrix` only when the matrix shows and does not fit, `%` only when the matrix shows, and `s` only on a team of more than one device.
- On a narrow terminal, the bar first names the status view alone, as `%` names share: `s status`, or `s ‹status›` when it shows. Then it drops keys from the least used: `%`, `r`, `s`, `←→`, the period, and scroll. It keeps `?` and `q` last. At 80 columns a team's matrix loses only `%`.

## Visual system

### Color

Semantic tokens, never raw colors in the code. Each has a value for dark and for light terminals. The report takes the terminal as light or dark as `COLORFGBG` says, else as the terminal answers when asked, else dark; it asks only from the terminal's foreground and only with no keys typed ahead. The interactive view starts as `COLORFGBG` says, else dark, and asks the terminal once it is open, so no key is lost; the answer then decides, even where `COLORFGBG` says otherwise.

| Token | Use |
| --- | --- |
| text | the terminal's own foreground |
| muted | column headers, secondary text, units |
| faint | tracks, rules, dots, unknown bars |
| accent | keys, pills, and the this-device mark |
| out, over, tight, ok, under | forecast states and their badges |
| heat 1–5 | the matrix ramp, from faint to bright and bold |

With `--color never`, `NO_COLOR`, or a pipe, the words and marks carry the meaning. In the matrix, the largest value in each column is bold instead of the heat map. A column with a `?` in it has none bold, since its largest is not known.

### Glyphs

`--ascii` swaps each glyph for a plain one.

| Glyph | Meaning | ASCII |
| --- | --- | --- |
| `━` `─` `┃` `╋` | used, left, and even pace in a bar | `=` `-` `\|` `+` |
| `┈` | a window with no reading | `.` |
| `●` | this device, or logged in here | `*` |
| `×` `~` `↓` | error, silent or old reading, old release | `x` `~` `v` |
| `·` | nothing, and separators | `.` |
| `≥` | at least: a total with a part that is not known | `>=` |
| `‹›` | the chosen option | `[]` |

### Text and numbers

- Section titles are bold capitals, followed by counts, with the alarming counts in their state color.
- Column headers are dim capitals.
- Durations: `7m`, `34m`, `5d 22h`, `1d 23h`. Clock times are local and 24-hour, with the weekday: `Fri 17:09`.
- Tokens: whole millions, `<1`, or `·`. Tokens that are not known are `?`, and a total with a part that is not known is `≥` before the part that is, rounded down: `≥68`.
- Emails show in full in SUBSCRIPTIONS and are cut in the middle when they do not fit. The matrix uses short names.

### Width

The page is laid out for 120 to 160 columns, and down to 80. As SUBSCRIPTIONS narrows, it drops LAST, then PLAN, then the reset's clock time, then the busiest user's name, and last the bar shrinks from 24 cells to 12.

The matrix's title drops the view pills before its modes, and the status view's title drops the unit before the view pills. As the status view narrows, NOTE is cut first, to as few as 16 columns. Then USER goes, then VIA, then TODAY, 30D, and 7D. The chosen period, 90D, DEVICE, VERSION, and SEEN stay. NOTE then takes what is left, and goes with less than 4 columns. At 80 columns the fixture keeps VERSION, SEEN, the four periods, and a short NOTE.

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
- In DEVICES: IN+OUT and the `cl cx gk hm` grid. The status view has the tokens of each period and VIA instead. What was wrong with a device goes to ATTENTION too.
- `--tokens`, which the matrix replaces.

## Build order

1. **Data.** Day buckets per account and project from the log timestamps. Tokens since each window began, per account and device. The larger snapshot and relay caps. JSON with the forecast of each window (percent, state, when it runs out) under a new schema version. The details are in [ai-report.md](ai-report.md).
2. **Forecast.** One function from a reading to its state, with tests for each state, stale readings, reset windows, and the first tenth of a window.
3. **Static report.** The page above on Lip Gloss, with golden files at 80, 120, and 160 columns, in color, and in ASCII.
4. **Interactive view.** On Bubble Tea, with the same renderer.
5. **Short names and `alias`.**
