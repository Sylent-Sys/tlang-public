import * as fs from "fs";
import * as path from "path";
import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind,
} from "vscode-languageclient/node";

// The bare name resolved against PATH when neither the `tlang.lsp.path`
// setting nor a bundled binary is available.
const defaultServerCommand = "tlang-lsp";

let client: LanguageClient | undefined;

// bundledServerPath returns the tlang-lsp binary shipped inside a
// platform-specific .vsix (<extensionPath>/bin/tlang-lsp[.exe]), or undefined
// when this install has none (a development checkout or a universal package).
function bundledServerPath(context: vscode.ExtensionContext): string | undefined {
  const exe = process.platform === "win32" ? "tlang-lsp.exe" : "tlang-lsp";
  const candidate = path.join(context.extensionPath, "bin", exe);
  if (!fs.existsSync(candidate)) {
    return undefined;
  }
  if (process.platform !== "win32") {
    ensureExecutable(candidate);
  }
  return candidate;
}

// ensureExecutable restores the exec bit if the package lost it on the way to
// disk. Failure is not fatal: the launch error below will name the binary.
function ensureExecutable(file: string): void {
  try {
    fs.accessSync(file, fs.constants.X_OK);
  } catch {
    try {
      const mode = fs.statSync(file).mode;
      fs.chmodSync(file, mode | 0o111);
    } catch {
      // Read-only install location; leave it for the launch to report.
    }
  }
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const configured = vscode.workspace
    .getConfiguration("tlang")
    .get<string>("lsp.path", "")
    .trim();
  const command =
    configured.length > 0
      ? configured
      : (bundledServerPath(context) ?? defaultServerCommand);

  const serverOptions: ServerOptions = {
    command,
    transport: TransportKind.stdio,
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ language: "tlang" }],
  };

  client = new LanguageClient(
    "tlang",
    "TLang Language Server",
    serverOptions,
    clientOptions,
  );

  try {
    await client.start();
  } catch (err) {
    void vscode.window.showErrorMessage(
      `TLang: could not start the language server (tried \`${command}\`). ` +
        "The extension looks for the server in this order: the `tlang.lsp.path` setting, " +
        "the tlang-lsp binary bundled with a platform-specific extension package, " +
        "then `tlang-lsp` on your PATH. Install the .vsix for your platform, " +
        "set `tlang.lsp.path`, or put `tlang-lsp` on your PATH. " +
        `(${err instanceof Error ? err.message : String(err)})`,
    );
  }
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  const stopping = client.stop();
  client = undefined;
  return stopping;
}
