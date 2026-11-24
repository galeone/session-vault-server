# Changelog

## [2.1.0](https://github.com/galeone/session-vault-server/compare/v2.0.5...v2.1.0) (2026-11-24)


### Features

* accept age+gzip+tar uploads for subagent transcript bundles ([#35](https://github.com/galeone/session-vault-server/issues/35)) ([34e8d3d](https://github.com/galeone/session-vault-server/commit/34e8d3d31a9c8534648f671528d5d7a347a2c3ae))

## [2.0.5](https://github.com/galeone/session-vault-server/compare/v2.0.4...v2.0.5) (2026-11-22)


### Bug Fixes

* **deb:** start the service again after an upgrade ([#32](https://github.com/galeone/session-vault-server/issues/32)) ([c675202](https://github.com/galeone/session-vault-server/commit/c675202391ed35943dc2228e0714d7d3baba5c39))

## [2.0.4](https://github.com/galeone/session-vault-server/compare/v2.0.3...v2.0.4) (2026-11-22)


### Bug Fixes

* **deb:** the .deb did not contain the binary ([#30](https://github.com/galeone/session-vault-server/issues/30)) ([5cd7669](https://github.com/galeone/session-vault-server/commit/5cd76693333d18730f8649bef894cd6aeefaf526))

## [2.0.3](https://github.com/galeone/session-vault-server/compare/v2.0.2...v2.0.3) (2026-11-22)


### Bug Fixes

* **deb:** build twice in a row; enable nothing at install, restart on upgrade ([#28](https://github.com/galeone/session-vault-server/issues/28)) ([b4d3101](https://github.com/galeone/session-vault-server/commit/b4d31011f2c971c709eeabdafa98f6c809bf28e0))

## [2.0.2](https://github.com/galeone/session-vault-server/compare/v2.0.1...v2.0.2) (2026-11-21)


### Bug Fixes

* restart the running service when the package is upgraded ([#26](https://github.com/galeone/session-vault-server/issues/26)) ([79f432d](https://github.com/galeone/session-vault-server/commit/79f432d0691e30c8157dea9f41d965b64ff712d4))

## [2.0.1](https://github.com/galeone/session-vault-server/compare/v2.0.0...v2.0.1) (2026-11-21)


### Bug Fixes

* build the deb in its source tree and unpack the rpm source ([#23](https://github.com/galeone/session-vault-server/issues/23)) ([afd1fbb](https://github.com/galeone/session-vault-server/commit/afd1fbb2e9b1fddc19f3f0b3c194ab926b63e44d))
* label packages built past the newest tag as X.Y.Z.rN.gHASH ([#25](https://github.com/galeone/session-vault-server/issues/25)) ([a48d9bd](https://github.com/galeone/session-vault-server/commit/a48d9bd21ce279bf0f88470f2297123b1a4cde04))

## [2.0.0](https://github.com/galeone/session-vault-server/compare/v1.7.0...v2.0.0) (2026-11-21)


### ⚠ BREAKING CHANGES

* uploads without X-Encrypted: age+gzip are rejected.

### Bug Fixes

* accept only [A-Za-z0-9_-] user names as storage namespaces ([#22](https://github.com/galeone/session-vault-server/issues/22)) ([34184c0](https://github.com/galeone/session-vault-server/commit/34184c05dceab71cb556759d6dc3278ae78c0500))
* review findings — path safety, duplicate session ids, log rotation, timeouts ([#20](https://github.com/galeone/session-vault-server/issues/20)) ([083abf2](https://github.com/galeone/session-vault-server/commit/083abf29b201a3ec8e04555745d63fff885b57f1))

## [1.7.0](https://github.com/galeone/session-vault-server/compare/v1.6.0...v1.7.0) (2026-11-19)


### Features

* offer an upgrade URL in the session-limit 402 ([#17](https://github.com/galeone/session-vault-server/issues/17)) ([13de38d](https://github.com/galeone/session-vault-server/commit/13de38d267812a503c910236f407b9d57b9402e3))

## [1.6.0](https://github.com/galeone/session-vault-server/compare/v1.5.0...v1.6.0) (2026-11-19)


### Features

* stamp vault_tokens.last_used_at on every authenticated request ([#15](https://github.com/galeone/session-vault-server/issues/15)) ([8e473e1](https://github.com/galeone/session-vault-server/commit/8e473e1e9e2ac34f5d69a19d89c11a82048415e9))

## [1.5.0](https://github.com/galeone/session-vault-server/compare/v1.4.0...v1.5.0) (2026-11-19)


### Features

* record stored sessions in vault_sessions for usage bookkeeping ([#13](https://github.com/galeone/session-vault-server/issues/13)) ([86edf28](https://github.com/galeone/session-vault-server/commit/86edf28065f6e955f5f7d106d031bc27d247a981))

## [1.4.0](https://github.com/galeone/session-vault-server/compare/v1.3.0...v1.4.0) (2026-11-18)


### Features

* cap uploads at 128 MB and max plan at 500 sessions ([#11](https://github.com/galeone/session-vault-server/issues/11)) ([1af6e9a](https://github.com/galeone/session-vault-server/commit/1af6e9a17f50a2d09d131c16633db16ed9d80f0f))

## [1.3.0](https://github.com/galeone/session-vault-server/compare/v1.2.1...v1.3.0) (2026-11-18)


### Features

* Postgres token source, per-plan quotas, account purge ([#9](https://github.com/galeone/session-vault-server/issues/9)) ([8352b97](https://github.com/galeone/session-vault-server/commit/8352b976d203211b56745573afc2d9d55db34085))

## [1.2.1](https://github.com/galeone/session-vault-server/compare/v1.2.0...v1.2.1) (2026-11-18)


### Bug Fixes

* resolve the package version from the newest reachable tag ([a9e684d](https://github.com/galeone/session-vault-server/commit/a9e684d265a4b8844156420378ce72fda4189224))
* resolve the package version from the newest reachable tag ([fd0c423](https://github.com/galeone/session-vault-server/commit/fd0c42331e1d63f173db2dd88188f10fc70cd99e))

## [1.2.0](https://github.com/galeone/session-vault-server/compare/v1.1.0...v1.2.0) (2026-11-17)


### Features

* add session-vault@.service systemd template unit ([912290e](https://github.com/galeone/session-vault-server/commit/912290ebdf587b159ed712477cecbef33ff0089d))
* use a single session-vault.service unit with hardcoded user ([e5eb6df](https://github.com/galeone/session-vault-server/commit/e5eb6dfb95dcae769884572dcd7f915cacce09ec))


### Bug Fixes

* install unit to usr/lib, skip dep install when tools present ([a176385](https://github.com/galeone/session-vault-server/commit/a176385dd2303823d3f33431246114cd49468129))
* make port and data dir overridable, fix DynamicUser unit ([fda446a](https://github.com/galeone/session-vault-server/commit/fda446aa94726451c8c87d67a644d5ca3105d6f8))
* makepkg is part of pacman, not a separate package ([9c8df2e](https://github.com/galeone/session-vault-server/commit/9c8df2ef364d1186e210f959178744ce6c28b855))
* use /etc/session-vault consistently in all packagings ([2fff4ab](https://github.com/galeone/session-vault-server/commit/2fff4abd01c0869deeb0fe9a5f72fca8d52313bf))
* use $(VAR) form for all make variable references ([7136f79](https://github.com/galeone/session-vault-server/commit/7136f7977ba7b7914c3e881b989050cef09dfa1a))

## [1.1.0](https://github.com/galeone/session-vault-server/compare/v1.0.0...v1.1.0) (2026-11-17)


### Features

* allow deleting a session (DELETE /api/v1/sessions/{id}) ([0d6f520](https://github.com/galeone/session-vault-server/commit/0d6f520c808fa7149a39a01d2f7d8db3ad170244))
* allow deleting a session (DELETE /api/v1/sessions/{id}) ([532afec](https://github.com/galeone/session-vault-server/commit/532afec3be0596c170e20d862eca4c9734d007fb))


### Bug Fixes

* check the meta-file removal error in the delete route ([1ca5fcd](https://github.com/galeone/session-vault-server/commit/1ca5fcd56f33c95d86915de77352fc3b3d41730e))

## 1.0.0 (2026-11-15)


### Features

* add slog structured logging with daily file output ([4505b8b](https://github.com/galeone/session-vault-server/commit/4505b8ba5ba566d57b6d547fba45c6bfea73739c))
* add tests, CI, linting and release-please workflows ([3238c10](https://github.com/galeone/session-vault-server/commit/3238c1045e2982bf7ae5582ac857aaa6e7884835))
