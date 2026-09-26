# Report design

The design of the console report, static and interactive. Decided on 2026-09-23 with the owner. [ai-report.md](ai-report.md) has the product requirements.

Built on 2026-09-24 for v0.2.0. Where the build decides what this text leaves open, the golden files in `internal/view/testdata/` show the result.

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

The page at 160 columns is the example at the top of [README.md](README.md), which is `internal/view/testdata/team-160.golden`; the golden files are exact where this text is not.

## Header

One line: `ai-usage`, this device, the team, and on the right the collector's health: collection, relay, and update. Each item is a colored dot and a few words. A failure turns its dot red, and the error itself goes to ATTENTION.

## ATTENTION

Shown only when something is wrong. At most 6 lines, then `+N more`; the interactive view shows all. One line each, most urgent first:

| Badge | When | Says |
| --- | --- | --- |
| `OUT` | a window is at 100% | when it comes back |
| `OVER` | a window will run out by its reset, at its pace so far | when it runs out, and how long before the reset |
| `ERROR` | a device's collector or one of its harnesses fails, or the release check of a device on an older release | the device and the error |
| `SILENT` | a device has not reported for a day | since when, and the error it last reported, if any |
| `OLD` | devices run an older release | all of them in one line, and, once one that is not silent has run its release for 7 hours without updating, for how long the longest has not updated |
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

The cell reads `157% over`, `81% ok`, or `out`.

### Users

USERS counts the devices with tokens on the account since the window began, and names the busiest. A Hermes bot in its own container is a device.

## DEVICES × SUBSCRIPTIONS

A matrix. Each device is a row, sorted by total, largest first. Each subscription is a column, grouped under its provider by a heading with a thin rule. The last column, NO QUOTA, holds tokens that have no subscription. Totals are on the right and at the bottom.

- A cell is input plus output tokens in the chosen period, in whole millions: `603` or `1210`. It is `<1` under a million and `·` with no use. Cache is left out.
- The cells form a heat map. A cell grows brighter, and at the top step bold, as its value grows, on a log scale relative to the largest cell. The largest consumers stand out without reading a number.
- A subscription's name in the header takes its state color, so an `out` column is visible from the matrix too.
- A mark before the device name gives its state: `●` this device, `×` an error, `~` silent, `↓` an old release. A silent device's error is the one it last reported, so it shows `~`.
- The share mode (`%`) shows each value as a percent of its column's total in the chosen period instead: a device's tokens on a subscription divided by the team's, so a column adds up to 100 and the bottom row shows that. TOTAL on the right is the device's part of all the team's tokens. It is how the tokens split, not how much of a quota the device used: SUBSCRIPTIONS shows how full each window is. The cells round to whole percents, `<1` under one and `>99` over 99 but under 100.

Many devices scroll down, and many subscriptions scroll sideways, with the device column and the headers kept in place: the interactive view keeps the title, the providers' line, and the subscriptions' names at the top of the page while the rows scroll under them. The static report prints the columns that fit and ends the header with `+N more`.

The section has two views, usage and status. The title names them as pills before the matrix's modes, `‹usage›  status   ‹tokens›  share`, where both pairs fit; where they do not, as at 80 columns, the views go and the modes stay.

With a single device, a matrix of one row says little. The section becomes USAGE: a row per subscription, with today, 7d, 30d, and 90d. It has one view, so `s` and `--devices` change nothing there.

### Status

The status view is DEVICES as a table of each device's state: which devices report, on which release, and what fails on them. `s` in the interactive view switches to it and back; `--devices` prints it and opens the interactive view on it.

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
| TODAY 7D 30D 90D | its input plus output tokens, as the matrix's TOTAL column prints them; their headers are dim, as every header is, and the title names the chosen period, as in `by 7d` |
| NOTE | for a silent device, `silent since` when it last reported, then the error it last reported, as ATTENTION says it; else what fails on it, in the out color; else, on an old release, `update failing:` and why its release check failed, in the out color; `not updated for 1d · latest v1.4.2`, in the tight color, once this device's reads of the team have found it reporting on that release for 7 hours, longer than v0.2.0 takes to update itself, counted from its first run they found after a newer release was out, so a machine asleep meanwhile is not counted; else `update: latest v1.4.2`; else, on a current release, `update check failing:` and why; else nothing. Only an error, and an update not made, are in color |

The bottom row is TOTAL for each period. With no note on any device, there is no NOTE column.

A short NOTE cuts an error with `…`, and keeps a silent device's last error, and why a release check failed, only while 12 columns of it fit. The check's note shortens what failed first, to keep why: `update:` for `update failing:`, `check failing:` for `update check failing:`; then it says only `update failing`, or `update check failing`, then `check failing`. It never cuts a time: a silent device's note becomes `since Mon 14:02`, then the day alone, as in `since Mon`, then `silent`. An old release's becomes `latest v1.4.2`, and an update not made `not updated for 1d`, then `not updated 1d`.

## PROJECTS

One table for this device, all accounts together, sorted by the chosen period (7d by default). Columns: the project, the period, 90d, sessions, the providers it used, and the last activity. The static report shows the top 10 and says how many more there are. `--projects` prints all of them.

A project is a git repository, named by its folder. Its subfolders and linked worktrees count under it while they exist, so one repository is one row however many branches it had. Claude Code worktrees still count under it after they are removed, and Codex worktrees too when one repository has their name. A folder outside any repository is its own project. [docs/json-schema.md](docs/json-schema.md#projects) has the rules.

## Legend

The static report ends with a dim legend, listing only the marks on screen, a word or two each:

```
━ used  ─ left  ┃╋ even use  ┈ no reading  ~ stale  ? unknown  — no forecast  ● here  × error  ↓ old  · none  ‹› chosen
```

With every mark on screen it is one line from 120 columns on. Below that, where one line does not fit, it takes as few lines as it can, of even length: two at 80 columns. The interactive view keeps the legend in `?` help, which says more of each mark.

## The interactive view

`ai-usage` opens it when standard input and output are both terminals. Piped output, `--json`, `--plain`, and a dumb terminal print the static report. So does the first run from the installer.

- It runs on the alternate screen, so the scrollback stays clean, and restores the terminal on exit, a crash included.
- The header stays at the top and the key bar at the bottom. The page scrolls between them.
- Scrolled into DEVICES, the page keeps the section's three lines over its rows at its top, in either view, as a sticky table header. It stays from the line its title would scroll off on, with the first row right under it the line before, until TOTAL comes up under it, and then lets go. A screen down starts under it with the line after the last one shown, so no row is skipped.
- It reflows on resize. Relative times tick, and the page reloads when a scheduled run saves new state.

```
 ↑↓ scroll · ←→ matrix · s ‹usage› status · p period ‹7d› · % share · r refresh · ? help · q quit
```

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

## The menu bar app

The popover answers the same questions as the page, in the same order, in 400 points. The report decides everything it shows; the app only lays it out. Its renders are `AIUsageBar --render <report.json> <folder>`.

- **The verdict line first.** The header is one sentence: what is out and when it is back, what will run out and when, or "All on track" with the subscription that has the most room. More of them count as "+N more". Without a reading, it says so. The dot beside it is the worst of the collector's health, with the report's age; the menu under it has the details.
- **Chips for what to fix.** Errors, devices not reporting, devices that stopped updating themselves, and health that is failing or unscheduled get a chip each, with a count. A device on an older release that still updates itself is lag after a release, not a chip. A problem of this Mac that a health chip names is not counted again among the errors. A click filters Usage to those devices, and the chosen chip shows it and clears it on a second click; a health chip opens the menu, which lists the chip's messages. Subscriptions do not get chips: the verdict and Limits have them.
- **Color means a problem.** Red is out, orange runs out, yellow is tight, and all else is gray: a window left mostly unused has a gray arrow, and this Mac a gray laptop. Every color also reads as a glyph or a word. The popover has no green, and no blue but its controls.
- **The projected bar.** A Limits bar is what is left, solid from the leading edge, as the percent beside it and the menu bar's ring are: it drains as the window fills. What the forecast says will be gone by the reset is cut from its end, faint, so the solid part is what will be left then. A forecast past 100% makes the whole fill faint, with a notch at the leading edge. An out window is an empty track tinted red. An old reading is dim, and so is the bar of an account that another window stops: its mark and countdown say why, in red.
- **A row is the account's state.** A Limits row shows its main window's bar and percent, and the account's state from the report: when another window stops it, the row takes that window's mark and counts down to when it is back, with the window named under it.
- **Details on click.** A row is one line of numbers. A click opens its details under it, one row at a time, as a few lines of a dim label and a value: each window in what is left and will be at its reset, the devices that used it, errors, releases, paths, and the tokens of each period as a small table with the chosen one bright. What the row shows is not said again. Tooltips carry who read a window, and what a word or glyph needs.
- **Projects by repository.** A project is a repository with its subfolders and worktrees. Projects next to each other in one folder show under its name, the ones on their own under "Other", in the report's order. Their last activity reads "20m ago", so it never passes for a countdown.
- **Nothing blinks or jumps.** The popover takes its height from Limits, the tab people open it for, and keeps it while it is open, so switching tabs, opening details, and "Show All" scroll inside it; Usage keeps its top line and its total in place as its list scrolls. Scrollers overlay the list, whatever System Settings says, and every tab lays its rows on one grid, so no column moves. It opens on the last tab, with every row closed.
- **Type.** Nothing smaller than 11 points. Numbers are monospaced digits, tokens are whole millions, and durations have at most two units, as on the page.

The menu bar item is a ring of what is left of the tightest window, drawn from 12 o'clock like a battery, with its percent left, the time until its reset, or nothing, as Settings picks. It is a template image, in the menu bar's own color, until a window runs out or will. One that will is an orange arc over an orange tint; one that is out, the most urgent, is the strongest mark, a solid red disc with a white cross, and the item shows the time until it is back.

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

The matrix's title drops the view pills before its modes, and the status view's title drops the unit before the view pills. As the status view narrows, NOTE is cut first, to as few as 16 columns. Then USER goes, then VIA, then TODAY, 30D, and 7D. The chosen period, 90D, DEVICE, VERSION, and SEEN stay. NOTE then takes what is left, and goes with less than 4 columns. At 80 columns the fixture keeps VERSION, SEEN, the four periods, and a short NOTE.

## Short names

- The matrix needs short account names. By default a name is the part of an email before the `@`, or the first 8 characters of an id. When two names collide within a provider, both show the full label.
- `ai-usage alias <account> <name>` names an account for the whole team, and `ai-usage alias <account> --clear` removes the name. The name travels sealed in the snapshot of the device that set it. If two devices set a name, the newer one wins.
