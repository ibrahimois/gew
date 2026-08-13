# GEW Source Control for VS Code

GEW Source Control adds explicit **GEW: Pull via REST**, **GEW: Push via REST**, and **GEW: Sync via REST** actions to VS Code's Source Control view. It is intended for GEW hybrid workspaces only.

## Requirements

- A workspace created with `gew clone --backend git` or migrated with `gew migrate --to git`.
- Both `.git/` and `.gew/` directories at the workspace root.
- GEW available on `PATH`, or configured through `gew.executablePath`.
- System Git when using VS Code's built-in local status, diff, stage, and commit UI. GEW itself does not invoke the system Git executable or Git transport.

VS Code's built-in Git extension continues to own local Source Control behavior through `.git`. GEW continues to use `.gew` as its synchronization journal and contacts forges only through HTTPS REST APIs.

## Install a local VSIX

From this repository:

```sh
cd extensions/vscode
npm ci
npm run package
code --install-extension ../../dist/gew-vscode-0.1.0.vsix --force
```

To uninstall:

```sh
code --uninstall-extension ibrahimois.gew-vscode
```

The extension is not published to the VS Code Marketplace and has no telemetry.

## Enable a repository

1. Open a hybrid repository, or a parent folder containing one, in VS Code.
2. Trust the workspace.
3. Select the repository in Source Control and run **GEW: Enable for Current Repository**, or invoke the command from the Command Palette while a file in that repository is active.

Enablement is stored locally in VS Code extension global state using the repository's canonical absolute path. Nested repositories discovered by VS Code remain available while their parent workspace folder is open. Nothing is added below `.gew`, `.git`, or `.vscode`, and enabling one repository does not enable another.

Run **GEW: Disable for Current Repository** from the Command Palette or the Source Control overflow menu to remove the local opt-in.

## Source Control actions

When the workspace is trusted:

- **GEW: Sync via REST** appears as a compact circular action on VS Code's existing Source Control view; no second Source Control page is added. It remains available before enablement so clicking it can explain how to enable a repository.
- Once at least one enabled hybrid repository is open, **GEW: Pull via REST**, **GEW: Push via REST**, and Disable appear in the Source Control overflow menu.
- Sync runs Pull first and runs Push only after Pull completes successfully.
- Progress and CLI output are available in the **GEW Source Control** OutputChannel.

These commands do not replace or redirect VS Code's built-in Git Pull, Push, or Sync commands. Built-in Git Sync still uses the repository's configured Git transport. Use only the actions whose names explicitly begin with `GEW:` when REST-only synchronization is required.

## Configure the executable

`gew.executablePath` is a machine-scoped setting. Its portable default is:

```json
"gew.executablePath": "gew"
```

A macOS installation may instead use an absolute path:

```json
"gew.executablePath": "/usr/local/bin/gew"
```

The extension starts the configured executable directly without a shell. It does not pass tokens, provider URLs, usernames, author details, environment dumps, or profile contents as command arguments.

## Troubleshooting

- **Restricted Mode / Workspace Trust:** GEW operations refuse to start until the workspace is trusted. This prevents repository-triggered process execution.
- **Executable not found:** install GEW or set `gew.executablePath` to the correct machine-local executable. Review the **GEW Source Control** OutputChannel for safe process diagnostics.
- **Repository is not hybrid:** clone with `gew clone --backend git`, or migrate a clean existing workspace with `gew migrate --to git`. A default `.gew`-only workspace cannot use these Source Control actions.
- **Dirty pull or merge failure:** resolve the condition using GEW's normal CLI workflow. The extension surfaces CLI failures and does not bypass workspace safety checks.
- **GitLab or Bitbucket push:** GEW 0.7.0 keeps these providers safety-gated until live concurrency verification passes. The extension reports that CLI failure and does not bypass the provider gate.
- **Multiple repositories:** the repository selected in Source Control is preferred. Otherwise the active editor is preferred when it belongs to an enabled repository; if the target is still ambiguous, the extension prompts instead of selecting arbitrarily.
- **Operation already running:** overlapping operations are blocked per canonical repository root, while different repositories may run independently.

## Development

Use Node.js 22 or newer. From `extensions/vscode/`:

```sh
npm ci
npm run compile
npm run typecheck
npm run lint
npm run test:unit
npm run test:integration
npm test
npm run package
```

Extension-host tests use isolated temporary user-data and extension directories. Automated tests do not run the real GEW binary, contact a forge, or use real credentials.
