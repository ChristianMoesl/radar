import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { dirname, resolve } from "node:path";
import { createHash } from "node:crypto";
import {
  FileError,
  ExecutionError,
  LineScanner,
  type ExecutionEnv,
  type FileInfo,
} from "@earendil-works/pi-durable/env";
import type { Context } from "@earendil-works/chord";
import type { LoadedSkill } from "./tools.ts";

export type WorkspaceBinding = {
  id: string;
  path: string;
  mode: "local" | "sbx";
  sandbox?: string;
};
const execute = promisify(execFile);

// This program runs inside the selected execution environment, including for
// filesystem access. Arguments are JSON argv, never interpolated shell text.
const filesystemProgram = String.raw`
const fs=require('node:fs/promises'),p=require('node:path');
const [op,cwd,encoded]=process.argv.slice(1),a=JSON.parse(encoded);
const path=x=>p.resolve(cwd,x),info=async x=>{const s=await fs.lstat(x);return {name:p.basename(x),path:x,kind:s.isDirectory()?'directory':s.isSymbolicLink()?'symlink':'file',size:s.size,mtimeMs:s.mtimeMs}};
const confined=async(root,x)=>{const r=await fs.realpath(path(root)),v=await fs.realpath(p.resolve(r,x)),rel=p.relative(r,v);if(rel==='..'||rel.startsWith('..'+p.sep)||p.isAbsolute(rel))throw Error('path outside selected read root');return v};
(async()=>{let v=null; switch(op){
case 'read': {const x=path(a[0]),s=await fs.stat(x);if(!s.isFile()||s.size>524288)throw Error('expected regular file at most 512 KiB');v=(await fs.readFile(x)).toString('base64');break}
case 'openBinary':{const x=path(a[0]),f=await fs.open(x,a[1]?require('node:fs').constants.O_RDONLY|require('node:fs').constants.O_NOFOLLOW:'r');try{const s=await f.stat();if(!s.isFile()||s.size>524288)throw Error('expected regular file at most 512 KiB');v={info:{name:p.basename(x),path:x,kind:'file',size:s.size,mtimeMs:s.mtimeMs},data:(await f.readFile()).toString('base64')}}finally{await f.close()}break}
case 'readOnly': {const x=await confined(a[0],a[1]),s=await fs.stat(x);if(!s.isFile()||s.size>524288)throw Error('expected regular file at most 512 KiB');v=await fs.readFile(x,'utf8');break}
case 'listOnly': {const x=await confined(a[0],a[1]);v=(await fs.readdir(x,{withFileTypes:true})).filter(e=>e.name!=='.git'&&e.name!=='node_modules').slice(0,1000).map(e=>e.name+(e.isDirectory()?'/':''));break}
case 'canonical': v=await fs.realpath(path(a[0]));break;
case 'info':v=await info(path(a[0]));break;
case 'list':v=await Promise.all((await fs.readdir(path(a[0]))).slice(0,1000).map(x=>info(p.join(path(a[0]),x))));break;
case 'exists':try{await fs.stat(path(a[0]));v=true}catch(e){if(e.code!=='ENOENT')throw e;v=false}break;
case 'write':await fs.writeFile(path(a[0]),Buffer.from(a[1],'base64'));break;
case 'append':await fs.appendFile(path(a[0]),Buffer.from(a[1],'base64'));break;
case 'truncate':await fs.truncate(path(a[0]),a[1]);break;
case 'flush':{const f=await fs.open(path(a[0]),'r+');try{await f.sync()}finally{await f.close()}break}
case 'rename':await fs.rename(path(a[0]),path(a[1]));break;
case 'mkdir':await fs.mkdir(path(a[0]),a[1]);break;
case 'remove':await fs.rm(path(a[0]),a[1]);break;
case 'tempdir':v=await fs.mkdtemp(p.join(cwd,a[0]||'.radar-tmp-'));break;
default:throw Error('unsupported filesystem operation');}
process.stdout.write(JSON.stringify(v));})().catch(e=>{process.stderr.write(e.message);process.exitCode=1});`;

export class WorkspaceExecution {
  readonly binding: WorkspaceBinding;
  private readonly controller = new AbortController();
  constructor(binding: WorkspaceBinding) {
    this.binding = binding;
  }
  close(): void {
    this.controller.abort(new Error("agent host is stopping"));
  }
  async exec(
    argv: readonly string[],
    cwd = this.binding.path,
    signal?: AbortSignal,
  ): Promise<string> {
    const { stdout } = await execute(
      process.env.RADAR_BINARY ?? "radar",
      [
        "workspace-exec",
        "--workspace",
        this.binding.path,
        "--cwd",
        cwd,
        "--json",
        "--",
        ...argv,
      ],
      {
        signal: signal
          ? AbortSignal.any([signal, this.controller.signal])
          : this.controller.signal,
        maxBuffer: 1024 * 1024,
        timeout: 120000,
      },
    );
    return (JSON.parse(stdout) as { output: string }).output;
  }
  async fs<T>(
    operation: string,
    args: unknown[],
    cwd = this.binding.path,
    signal?: AbortSignal,
  ): Promise<T> {
    return JSON.parse(
      await this.exec(
        ["node", "-e", filesystemProgram, operation, cwd, JSON.stringify(args)],
        cwd,
        signal,
      ),
    ) as T;
  }
  readOnly(root: string, path: string, signal?: AbortSignal): Promise<string> {
    return this.fs("readOnly", [root, path], this.binding.path, signal);
  }
  listOnly(
    root: string,
    path: string,
    signal?: AbortSignal,
  ): Promise<string[]> {
    return this.fs("listOnly", [root, path], this.binding.path, signal);
  }
  async loadSkill(path: string): Promise<LoadedSkill> {
    if ((await this.fs<FileInfo>("info", [path])).kind === "directory")
      path = resolve(path, "SKILL.md");
    path = await this.fs<string>("canonical", [path]);
    const source = await this.readOnly(dirname(path), path);
    const name =
      /^name:\s*["']?([^\n"']+)/m.exec(source)?.[1]?.trim() ??
      dirname(path).split("/").at(-1)!;
    return {
      name,
      path,
      root: dirname(path),
      hash: createHash("sha256").update(source).digest("hex"),
      instructions: `Skill ${name}\nSource: ${path}\nResolve resources relative to ${dirname(path)}.\n\n${source}`,
    };
  }

  /** Bounded filesystem and command adapter for native durable tools. Unsupported
   * streaming/watch operations fail explicitly rather than touching host files. */
  native(cwd = this.binding.path): ExecutionEnv {
    const self = this;
    const ok = <T>(value: T) => ({ ok: true as const, value });
    const fail = (error: unknown) => ({
      ok: false as const,
      error: new FileError("unknown", String(error)),
    });
    const fs = async (op: string, args: unknown[]) => {
      try {
        return ok(await self.fs(op, args, cwd));
      } catch (error) {
        return fail(error);
      }
    };
    const methods: Record<string, any> = {
      id: `radar:${this.binding.id}:${this.binding.mode}`,
      cwd,
      absolutePath: async (path: string) => ok(resolve(cwd, path)),
      joinPath: async (parts: string[]) => ok(resolve(...parts)),
      readBinaryFile: async (path: string) => {
        try {
          return ok(
            Buffer.from(await self.fs<string>("read", [path], cwd), "base64"),
          );
        } catch (error) {
          return fail(error);
        }
      },
      readTextFile: async (path: string) => {
        try {
          return ok(
            Buffer.from(
              await self.fs<string>("read", [path], cwd),
              "base64",
            ).toString("utf8"),
          );
        } catch (error) {
          return fail(error);
        }
      },
      readTextLines: async (path: string, options?: { maxLines?: number }) => {
        try {
          return ok(
            Buffer.from(await self.fs<string>("read", [path], cwd), "base64")
              .toString("utf8")
              .split("\n")
              .slice(0, options?.maxLines),
          );
        } catch (error) {
          return fail(error);
        }
      },
      openBinaryReader: async (
        path: string,
        options?: { noFollow?: boolean },
      ) => {
        try {
          const snapshot = await self.fs<{ info: FileInfo; data: string }>(
            "openBinary",
            [path, options?.noFollow ?? false],
            cwd,
          );
          const data = Buffer.from(snapshot.data, "base64");
          let closed = false;
          return ok({
            info: async () => ok(snapshot.info),
            read: async (offset: number, length: number) =>
              closed
                ? fail("reader closed")
                : ok(data.subarray(offset, offset + length)),
            scanLines: async (options: {
              startLine: number;
              endLine?: number;
            }) => {
              const scanner = new LineScanner(
                options.startLine,
                options.endLine,
              );
              scanner.push(data);
              return ok(scanner.finish());
            },
            close: async () => {
              closed = true;
            },
          });
        } catch (error) {
          return fail(error);
        }
      },
      openDirReader: async (path: string) => {
        try {
          const entries = await self.fs<FileInfo[]>("list", [path], cwd);
          let offset = 0;
          return ok({
            next: async (maxEntries: number) => {
              const page = entries.slice(offset, offset + maxEntries);
              offset += page.length;
              return ok({ entries: page, done: offset >= entries.length });
            },
            close: async () => {
              offset = entries.length;
            },
          });
        } catch (error) {
          return fail(error);
        }
      },
      canonicalPath: (path: string) => fs("canonical", [path]),
      fileInfo: (path: string) => fs("info", [path]),
      exists: (path: string) => fs("exists", [path]),
      listDir: (path: string) => fs("list", [path]),
      writeFile: (path: string, content: string | Uint8Array) =>
        fs("write", [path, Buffer.from(content).toString("base64")]),
      appendFile: (path: string, content: string | Uint8Array) =>
        fs("append", [path, Buffer.from(content).toString("base64")]),
      truncateFile: (path: string, size: number) =>
        fs("truncate", [path, size]),
      flushFile: (path: string) => fs("flush", [path]),
      renameFile: (a: string, b: string) => fs("rename", [a, b]),
      createDir: (path: string, options: unknown) =>
        fs("mkdir", [path, options ?? {}]),
      remove: (path: string, options: unknown) =>
        fs("remove", [path, options ?? {}]),
      createTempDir: (prefix?: string) => fs("tempdir", [prefix]),
      cleanup: async () => {},
      exec: async (
        command: string | readonly string[],
        options: any,
        context: Context,
      ) => {
        if (options?.env || options?.inheritEnv === false || options?.spill)
          return {
            ok: false,
            error: new ExecutionError(
              "unknown",
              "environment overrides and spill files are not supported by the bounded Radar adapter",
            ),
          };
        try {
          const text = await self.exec(
            typeof command === "string" ? ["sh", "-lc", command] : command,
            options?.cwd ?? cwd,
            context.abortSignal,
          );
          options?.onOutput?.(text, context, { stream: "stdout" });
          return ok({ exitCode: 0 });
        } catch (error) {
          return {
            ok: false,
            error: new ExecutionError("spawn_error", String(error)),
          };
        }
      },
    };
    return new Proxy(methods, {
      get: (target, key) =>
        key === "then"
          ? undefined
          : key in target
            ? target[key as string]
            : async () => ({
                ok: false,
                error: new FileError(
                  "not_supported",
                  `${String(key)} is unavailable in Radar's bounded workspace adapter`,
                ),
              }),
    }) as ExecutionEnv;
  }
}
