# Agent instructions

## Versioning

Always bump `version` in `mygo.json` before you build, so every build has its own version and it is clear which one is installed and running.

- Bump it for every change that is built to be tried, not only for releases. Never rebuild under a version that was already built.
- Patch (`0.4.3` to `0.4.4`) for fixes and small tweaks, minor (`0.4.x` to `0.5.0`) for a new feature.
- The version is only in `mygo.json`. It names the installer: `build/windows-amd64/armoury-crate-fix-setup-<version>-amd64.exe`.
- After a build, tell the user the new version and the installer path, and that the running copy has to be exited from the tray before installing it.
