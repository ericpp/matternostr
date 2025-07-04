# matternostr

A bridge plugin for [Matterbridge](https://github.com/42wim/matterbridge) that relays Nostr Live Chat messages (NIP-53) between Nostr and other chat platforms supported by Matterbridge (IRC, Discord, Telegram, etc).

The plugin monitors an `npub` for Live Events and automatically attaches to any events that become live and detaches from events that are no longer live.

## Requirements

- Go 1.24+
- A running [Matterbridge](https://github.com/42wim/matterbridge) instance
- Nostr keys (public and private)
- At least one Nostr relay

## Configuration

Create a `matternostr.toml` file (see `matternostr.example.toml`):

```toml
# Matterbridge API Configuration
matterbridge_url = "http://localhost:4242"
api_token = "somepassword"
gateway = "nostrrelay"

# Nostr public/private to use to relay messages
public_key = "npub1234567..."
private_key = "nsec1234567..."

# Nostr npub to monitor for new live events
watch_npub = "npub987654..."

# Default relays to connect to
default_relays = [
    "wss://relay.damus.io",
    "wss://nos.lol",
    "wss://relay.nostr.band"
]

# Max attempts to connect to a relay before timing out
max_retry_attempts = 3

# Relay timeout interval in minutes
retry_timeout_minutes = 10
```


Update your Matterbridge `matterbridge.toml` file (see `matterbridge.example.toml`):

```toml
[api]
[api.nostr]
BindAddress="127.0.0.1:4242"
Buffer=1000
Token="somepassword"
Label=""
RemoteNickFormat="<{NOPINGNICK}> "

[irc.zeronode]
Server="irc.zeronode.net:6697"
Nick="NostrRelay"
UseTLS=true
SkipTLSVerify=false
RemoteNickFormat="<{NOPINGNICK}> "

[[gateway]]
name="nostrrelay"
enable=true

[[gateway.inout]]
account="irc.zeronode"
channel="#nostrrelay"

[[gateway.inout]]
account="api.nostr"
channel="api"
```

## Usage

### Build and run locally

```sh
go build -o matternostr
./matternostr -conf matternostr.toml
```

- Use `-debug` for verbose logging.

### Docker

Build and run with Docker:

```sh
docker build -t matternostr .
docker run --rm -v $(pwd)/matternostr.toml:/app/matternostr.toml matternostr
```

## Command-line options

- `-conf <file>`: Path to config file (default: `matternostr.toml`)
- `-debug`: Enable debug logging

## Dependencies

- [github.com/nbd-wtf/go-nostr](https://github.com/nbd-wtf/go-nostr)
- [github.com/sirupsen/logrus](https://github.com/sirupsen/logrus)
- [github.com/spf13/viper](https://github.com/spf13/viper)

See `go.mod` for the full list.

## License

MIT
