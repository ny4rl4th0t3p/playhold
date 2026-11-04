import {execFile, spawn} from "node:child_process";
import {mkdtemp, rm} from "node:fs/promises";
import {tmpdir} from "node:os";
import {join, resolve} from "node:path";
import {createInterface} from "node:readline";
import {promisify} from "node:util";

import type {TestProject} from "vitest/node";

declare module "vitest" {
    export interface ProvidedContext {
        serverUrl: string;
    }
}

const repoRoot = resolve(import.meta.dirname, "../../..");

/** Builds and starts the Go test server. It's built first because killing `go run` would leave the server running. */
export default async function setup(project: TestProject): Promise<() => Promise<void>> {
    const dir = await mkdtemp(join(tmpdir(), "playhold-testserver-"));
    const binary = join(dir, "testserver");
    await promisify(execFile)("go", ["build", "-o", binary, "./internal/testserver"], {cwd: repoRoot});

    const server = spawn(binary, {stdio: ["ignore", "pipe", "inherit"]});
    const url = await new Promise<string>((resolveUrl, reject) => {
        server.once("error", reject);
        server.once("exit", (code) => reject(new Error(`test server exited with code ${code}`)));
        createInterface({input: server.stdout}).once("line", resolveUrl);
    });
    project.provide("serverUrl", url);

    return async () => {
        server.removeAllListeners("exit");
        server.kill();
        await rm(dir, {recursive: true, force: true});
    };
}