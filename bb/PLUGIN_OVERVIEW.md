AI Usage shows every Claude Code, Codex and Grok subscription your team shares, on a page in BB: how much of each limit is left, when it runs out at the current pace, which machine used it, and what the subscriptions cost a month. The numbers come from ai-usage, an open-source collector that runs on each of your team's machines.

## What you get

- **Attention first.** A window that is out, one that will run out before its reset, a machine that stopped reporting or whose tool fails, an old release, a subscription nobody uses.
- **Subscriptions.** Each one's limiting windows, with what is left, when they reset, and the forecast at reset.
- **Devices × subscriptions.** Weighted tokens of every machine on every subscription, today, over 7, 30 or 90 days.
- **Cost.** The team's subscriptions a month, how much of them the team used over 4 weeks, the next charge, and the questions ai-usage asks, answered on the page.
- **Team usage** in the sidebar footer: the top three alerts and a bar for each subscription.
- **Invite and Join.** Invite copies a message with the team key; Join pastes a colleague's key.

An example team of made-up data is on the page before ai-usage is set up.

## What it installs

When the plugin starts on a Mac or Linux machine that runs BB as a person, not root, and has no ai-usage, it installs ai-usage there once, with the install.sh of ai-usage's latest GitHub release:

- the `ai-usage` binary in `~/.local/bin`, which the system scheduler (launchd or cron) runs every 15 minutes, with BB open or not;
- the binary updates itself from GitHub Releases;
- your shell profile and the menu bar app are left alone; Settings → AI Usage can add the app on macOS 14 and later.

A machine that already has ai-usage, from the installer or Homebrew, is used as it is, and nothing is installed. Settings → AI Usage lists your other BB machines, each with an Install that joins it to the same team.

## What leaves your machines

Each machine sends a signed snapshot to a relay, by default the one ai-usage's maintainer runs. The relay sees tools, plans, percentages and token counts. Names, emails and project paths are encrypted with the team key, which stays on your machines.

## Good to know

- Removing the plugin leaves ai-usage collecting. Settings → AI Usage → Remove ai-usage takes it off a machine first.
- ai-usage reads Claude Code, Codex, Grok and Hermes. BB's Cursor, OpenCode and Pi agents are not counted.
- Sessions that BB's Account Pooler routes count for the login of the folder that holds their logs, which may not be the account that served them. Such a machine is marked "Account Pooler" on the page. A machine that runs only pooled Claude Code is marked after a run inside a BB thread.
- Anything that runs in this BB as you, agents included, can read the team report through this plugin and ask it for the team key, as it can read ai-usage's own files. The plugin answers no other plugin, keeps no copy of the key, and logs neither the key nor the report.
- Clocks in the report are in the time zone of the machine BB runs on.
- Notifications stay with ai-usage's menu bar app; this plugin sends none.
- Windows machines are not supported by the plugin yet; ai-usage installs there with install.ps1 and shows its own report.

Source, issues and the full README: https://github.com/neoromantic/ai-usage
