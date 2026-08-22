# Program updates

[← Plugin overview](README.md)　·　[← Back to README](../../../README.en.md)　·　[中文](../../plugins/self-update.md)　·　English

## Program updates (self_update plugin)

The program updates itself: once a day it checks GitHub for a new stable release in the background, and if there is one, the gear under "Maintenance · Program updates" on the settings page downloads, verifies, replaces and restarts in one click. All three platforms are supported, with no package manager and no unpacking over the old copy.

The button says what happens next. Before it knows whether there is a new version, it reads "check for updates", and clicking only checks, laying out the version number and the release notes. Once a new version is found it becomes "update to vX.Y.Z and restart", and only a second click actually does anything. That one step of difference is the confirmation.

An update does these things in order, and if any of them fails the whole thing stops with the program file untouched:

1. Confirm the install directory is writable (an installation in `/usr/local/bin` or `Program Files`, or one made by a package manager, stops here with a note to upgrade the way you installed it)
2. Download the release for this machine's OS and architecture, and verify it against the `SHA256SUMS.txt` in the release
3. Extract the new binary and **run `wen version` with it first** — a matching checksum does not mean it will start on this machine (a wrong architecture, or antivirus having rewritten it, fails right here)
4. Replace the program file. On Linux and macOS it is swapped out directly (the running process is unaffected); on Windows the old one is renamed to `wen.exe.old` and cleaned up at the next start
5. Restart. On Linux and macOS the process image is replaced in place (the PID does not change); on Windows a new process starts and the old one exits

The UI disconnects for a few seconds during the restart and reconnects on its own, and the progress window goes from "restarting" to "updated to vX.Y.Z". **A restart interrupts any conversation in progress**, and for remote access it invalidates your sign-in as well (tokens live in memory only), so you have to sign in again. If you would rather it not restart on its own, turn "restart after updating" off; the update still happens and takes effect the next time you start it yourself.

There are only three settings: whether to check automatically (on by default), how often (24 hours by default), and whether to restart after updating (on by default). Automatic checking **only checks** — installing is always your click. The whole plugin can also be turned off, in which case it does not even check. It gives the model nothing: no prompt, no tools; the character has no idea any of this exists.

On the command line it is `wen update`: with no arguments it only reports, `--check` only checks, and `-y` actually replaces the program file. It will not restart a service running in another process, only tell you to restart — for the one-step version, use the button on the settings page.

Only stable releases count: the rolling `dev` prerelease is skipped (its tag is always `dev`, so there is no version number to compare), and while running a development build (`v0.6.1-3-gxxxxxxx`) a stable release of the same number is not treated as an upgrade — it would be a downgrade.

A checksum guarantees that what you downloaded is not corrupt, not that the release itself was not tampered with; the trust anchors are TLS and the GitHub account.
