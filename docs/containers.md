# Running ai-usage in a container

A container is a machine in the team like any other. Install the collector inside it, as the user whose tools it should read, and it reads that user's Claude, Codex, Grok, and Hermes folders as it would on a laptop. A bot in each container is then a machine of its own in the team, under its own name.

Three things differ from a machine:

| Need | In a container |
| --- | --- |
| a state folder that lasts | the device id, the team key, and the ledger live in it; a new container without them is a new machine |
| a name | the host name is random |
| a scheduler | there is rarely cron |

## Keep the home

Keep the user's home on a volume or a bind mount, or point `AI_USAGE_HOME` at one. The installer puts the binary in `~/.local/bin` and the state in `~/.config/ai-usage`, so both outlive the container. Keep the binary there rather than in the image: the collector updates itself, which it can only do where it can write, and a copy in the image goes back to the image's version in every new container.

## Install

Install once into the running container, as the user. The team key goes through standard input, not the command line:

```sh
ai-usage team key | docker exec -i -u app mybot sh -c 'key=$(cat)
  url=https://raw.githubusercontent.com/neoromantic/ai-usage/main/install.sh
  script=$(curl -fsSL "$url" || wget -qO- "$url") &&
    printf "%s\n" "$script" | AI_USAGE_NAME=mybot AI_USAGE_TEAM_KEY="$key" sh'
```

The installer needs `curl` or `wget`, and `sha256sum`, `shasum`, or `openssl`; the script is fetched in full first, so a container with neither tool fails instead of installing nothing. `docker exec -u` takes `HOME` from the image when it sets one, so add `-e HOME=/home/app` if the image's `HOME` is another user's. Add `AI_USAGE_RELAY` if the team uses its own relay. `AI_USAGE_NAME` in the container's environment names the machine too, and overrides the name saved at install.

Its first run collects and prints the report. The report says the collector is not scheduled, because there is no crontab. That is expected until `ai-usage schedule run` runs.

## Keep it running

`ai-usage schedule run` collects at once and then every 15 minutes, at the quarter hours, until it is stopped. Each collection is a new process of the binary on disk, so an update takes effect at the next one. While it runs, other runs in the container do not try to register with a system scheduler, and `ai-usage status` shows the schedule as `every 15 minutes by ai-usage schedule run`. It stops on `SIGTERM` or `SIGINT` and passes `SIGTERM` on to a collection in progress, which stops without leaving its files half written.

Run it as the same user, beside the container's main process.

### From an entrypoint

```sh
#!/bin/sh
# Collect usage beside the main process once ai-usage is installed.
if [ -x /home/app/.local/bin/ai-usage ]; then
  /home/app/.local/bin/ai-usage schedule run &
fi
exec "$@"
```

If the entrypoint runs as root and the main process as another user, start `schedule run` as that user, with that user's `HOME`, for example with `su-exec app` or `env HOME=/home/app setpriv --reuid app --regid app --init-groups`. Without its `HOME` the collector looks for its state folder and the tools' folders in root's home.

### Under s6-overlay

Images built on [s6-overlay](https://github.com/just-containers/s6-overlay) version 3 supervise it as a service. Four files make it one:

```
/etc/s6-overlay/s6-rc.d/ai-usage/type                  longrun
/etc/s6-overlay/s6-rc.d/ai-usage/run                   the script below
/etc/s6-overlay/s6-rc.d/ai-usage/dependencies.d/base   empty
/etc/s6-overlay/s6-rc.d/user/contents.d/ai-usage       empty
```

```sh
#!/command/with-contenv sh
# Collects AI usage every 15 minutes once ai-usage is installed in the
# user's home, which outlives the container.
bin=/home/app/.local/bin/ai-usage
while [ ! -x "$bin" ]; do sleep 600; done
exec env HOME=/home/app s6-setuidgid app "$bin" schedule run
```

In a Dockerfile:

```dockerfile
COPY ai-usage.run /etc/s6-overlay/s6-rc.d/ai-usage/run
RUN chmod 0755 /etc/s6-overlay/s6-rc.d/ai-usage/run \
    && echo longrun > /etc/s6-overlay/s6-rc.d/ai-usage/type \
    && mkdir -p /etc/s6-overlay/s6-rc.d/ai-usage/dependencies.d \
    && touch /etc/s6-overlay/s6-rc.d/ai-usage/dependencies.d/base \
       /etc/s6-overlay/s6-rc.d/user/contents.d/ai-usage
```

The service waits until the collector is installed, so the image can ship before any container has it. s6 restarts `schedule run` if it exits.

## Check it

```sh
docker exec -u app mybot /home/app/.local/bin/ai-usage status
```

`schedule` should say `every 15 minutes by ai-usage schedule run`, and `relay` when it last pushed. On any machine in the team, `ai-usage --devices` lists the container under its name.

## Moving a folder from another collector

A collector on the host may have read the container's folders before, added with `ai-usage home add`. Once the container collects them itself, remove them on the host with `--forget`:

```sh
ai-usage home remove hermes /srv/bots/mybot/.hermes --forget
```

The container's first run counts the folders' history again, from the last 90 days. `--forget` drops the sessions the host counted from them, with the profiles inside them, so the team does not count that history twice. It waits for a collection in progress, and the team sees the change after the host's next collection. Without it, the host keeps those sessions until they are 90 days old.
