# Security policy

Xhaos runs AI agents that execute commands on the machine it runs on, and it stores API keys and secrets. Security reports are taken seriously.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through GitHub: on the repository's **Security** tab, choose **Report a vulnerability**. Include what an attacker can do, the steps to reproduce it, and the version or commit you tested.

You can expect an acknowledgement within a week. Once a fix is ready, it is released and the advisory published, crediting you unless you prefer otherwise.

## Supported versions

Xhaos is in early development: fixes go to the latest release and `main` only.

## Scope

In scope, for example:

- reaching a workspace without its passphrase, or one workspace's session reaching another workspace's data;
- an agent's command escaping the sandbox (reading outside its project, reaching sockets it should not);
- a secret's value reaching the model, the transcript, or a host it is not allowed to reach;
- a tool call running without the approval its policy requires;
- decrypting stored keys without the passphrase.

Out of scope:

- anything that needs the passphrase of the workspace concerned, or access to the server as its user or root;
- an agent doing what you approved;
- commands run without the sandbox, where the kernel or container does not support Landlock (Settings → Tools reports this);
- MCP servers you configure, which run unsandboxed by design;
- denial of service by a signed-in user.

The [README](README.md#security) and [docs/deploy.md](docs/deploy.md) describe the security model and how to deploy Xhaos safely.
