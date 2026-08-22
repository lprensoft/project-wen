# CLI

[← Back to README](../../README.en.md)　·　[中文](../cli.md)　·　English

## Subcommands

```
wen                          start the server (same as wen serve)
wen serve [-c config] [-p port]  start the server
wen config [section] [-c config] guided configuration (sections: plugins / models / server)
wen status [-c config]           print the current configuration and runtime state
wen eval <script.json> [-o file] replay a scripted conversation and report on style and consistency
wen update [--check] [-y]    check for and install a new version
wen version                  print the version
wen help [command]           show help
```

## wen config: changing plugin settings from a terminal

`wen config` exists for **remote deployments**. Plugin toggles and parameters do not live in config.yaml, and until now the only place to change them was the web UI — which is not always convenient to open on a remote machine. It has two modes, and the one in use is stated at the top of the screen:

| Service state | Mode | Behaviour |
|---|---|---|
| Running | Online | Changes go through the service's API and **take effect immediately**; no restart |
| Not running | Offline | The config file is edited directly, and the changes apply at the next start |

Why going through the API is mandatory while the service is up: the plugin state file is rewritten wholesale by the server from memory, so an outside edit neither takes effect nor survives — the server's next write erases it. The mode is decided by `wen.lock` in the config directory (which records the address actually being listened on), confirmed by really connecting once, so a stale record left behind by a killed process does not mislead it.

In offline mode plugins are **registered but not initialized**: reading their config-field declarations and changing toggles and parameters does not require them to actually run, whereas initializing them as usual would bring up every chat channel's long-lived connection along with the heartbeat and the scheduler — and all you wanted was to change one number.

The form is generated from the config fields the plugins declare themselves, the same source the settings page uses, so a new plugin gets both interfaces at once. Plugin actions are here too: select the WeChat channel and a "bind by QR code" action appears, with the code drawn as text right in the terminal (black and white regardless of your color scheme, so it scans under a dark theme too), the raw link printed underneath as a fallback, and an automatic refresh and redraw when it expires — no need to open the web UI on a remote server just to scan a code.
